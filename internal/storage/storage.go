package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, migrationV1); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

type Session struct{ ID, ProfileID, Source, ExternalRef, Title, CreatedAt, UpdatedAt string }
type Message struct {
	ID                         int64
	SessionID, Role, CreatedAt string
	Content                    []byte
}
type ToolRun struct {
	ID, SessionID, ToolName, Status, StartedAt, EndedAt string
	Args, Result                                        []byte
}
type Memory struct{ ID, ProfileID, Scope, Content, Tags, CreatedAt, UpdatedAt string }
type Skill struct {
	ID, ProfileID, Name, Path, Description, Tags string
	Enabled                                      bool
}
type Approval struct {
	ID, SessionID, Risk, Status, DecidedAt string
	Request                                []byte
}
type MCPServer struct {
	ID, ProfileID, Name, Transport string
	Config                         []byte
	Enabled                        bool
}

type Sessions interface {
	Create(context.Context, Session) error
	Get(context.Context, string) (Session, error)
	Touch(context.Context, string) error
}
type Messages interface {
	Append(context.Context, Message) (int64, error)
	ListBySession(context.Context, string) ([]Message, error)
}
type ToolRuns interface {
	Create(context.Context, ToolRun) error
	Finish(ctx context.Context, id, status string, result []byte) error
}
type Memories interface {
	Upsert(context.Context, Memory) error
	List(ctx context.Context, profileID, scope string) ([]Memory, error)
}
type Skills interface {
	Upsert(context.Context, Skill) error
	ListEnabled(ctx context.Context, profileID string) ([]Skill, error)
}
type Approvals interface {
	Create(context.Context, Approval) error
	Decide(ctx context.Context, id, status string) error
}
type MCPServers interface {
	Upsert(context.Context, MCPServer) error
	ListEnabled(ctx context.Context, profileID string) ([]MCPServer, error)
}

const migrationV1 = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS profiles(id TEXT PRIMARY KEY, name TEXT NOT NULL, root_path TEXT, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, profile_id TEXT NOT NULL, source TEXT NOT NULL, external_ref TEXT, title TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content_json BLOB NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tool_runs(id TEXT PRIMARY KEY, session_id TEXT, tool_name TEXT NOT NULL, args_json BLOB NOT NULL, result_json BLOB, status TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT);
CREATE TABLE IF NOT EXISTS memories(id TEXT PRIMARY KEY, profile_id TEXT NOT NULL, scope TEXT NOT NULL, content TEXT NOT NULL, tags_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS skills(id TEXT PRIMARY KEY, profile_id TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL, description TEXT, tags_json TEXT, enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS approvals(id TEXT PRIMARY KEY, session_id TEXT, risk TEXT NOT NULL, request_json BLOB NOT NULL, status TEXT NOT NULL, decided_at TEXT);
CREATE TABLE IF NOT EXISTS mcp_servers(id TEXT PRIMARY KEY, profile_id TEXT NOT NULL, name TEXT NOT NULL, transport TEXT NOT NULL, config_json BLOB NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
`

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (db *DB) CreateSession(ctx context.Context, s Session) error {
	now := Now()
	if s.CreatedAt == "" {
		s.CreatedAt = now
	}
	if s.UpdatedAt == "" {
		s.UpdatedAt = now
	}
	_, err := db.ExecContext(ctx, `INSERT INTO sessions(id, profile_id, source, external_ref, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, s.ID, s.ProfileID, s.Source, s.ExternalRef, s.Title, s.CreatedAt, s.UpdatedAt)
	return err
}
func (db *DB) GetSession(ctx context.Context, id string) (Session, error) {
	var s Session
	err := db.QueryRowContext(ctx, `SELECT id, profile_id, source, external_ref, title, created_at, updated_at FROM sessions WHERE id=?`, id).Scan(&s.ID, &s.ProfileID, &s.Source, &s.ExternalRef, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}
func (db *DB) TouchSession(ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET updated_at=? WHERE id=?`, Now(), id)
	return err
}

func (db *DB) AppendMessage(ctx context.Context, m Message) (int64, error) {
	if m.CreatedAt == "" {
		m.CreatedAt = Now()
	}
	res, err := db.ExecContext(ctx, `INSERT INTO messages(session_id, role, content_json, created_at) VALUES (?, ?, ?, ?)`, m.SessionID, m.Role, m.Content, m.CreatedAt)
	if err != nil {
		return 0, err
	}
	_ = db.TouchSession(ctx, m.SessionID)
	return res.LastInsertId()
}
func (db *DB) AppendJSONMessage(ctx context.Context, sessionID, role string, v any) (int64, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	return db.AppendMessage(ctx, Message{SessionID: sessionID, Role: role, Content: b})
}
func (db *DB) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, role, content_json, created_at FROM messages WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) UpsertMemory(ctx context.Context, m Memory) error {
	now := Now()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	_, err := db.ExecContext(ctx, `INSERT INTO memories(id, profile_id, scope, content, tags_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET content=excluded.content, tags_json=excluded.tags_json, updated_at=excluded.updated_at`, m.ID, m.ProfileID, m.Scope, m.Content, m.Tags, m.CreatedAt, m.UpdatedAt)
	return err
}
func (db *DB) ListMemories(ctx context.Context, profileID, scope string) ([]Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, profile_id, scope, content, tags_json, created_at, updated_at FROM memories WHERE profile_id=? AND (?='' OR scope=?) ORDER BY updated_at`, profileID, scope, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.ProfileID, &m.Scope, &m.Content, &m.Tags, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

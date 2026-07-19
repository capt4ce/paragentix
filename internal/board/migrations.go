package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (a *App) migrate() error {
	_, e := a.DB.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,email TEXT UNIQUE NOT NULL,password_hash BLOB NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS auth_sessions(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,token_hash TEXT UNIQUE NOT NULL,expires_at TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS user_settings(user_id INTEGER PRIMARY KEY REFERENCES users ON DELETE CASCADE,default_cli TEXT NOT NULL DEFAULT 'codex',workspace_root TEXT NOT NULL,hermes_url TEXT NOT NULL DEFAULT '',hermes_api_key TEXT NOT NULL DEFAULT '',hermes_model TEXT NOT NULL DEFAULT 'hermes-agent',updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS lanes(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,position INTEGER NOT NULL,paused INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,position));
CREATE TABLE IF NOT EXISTS jobs(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,lane_id INTEGER NOT NULL REFERENCES lanes ON DELETE CASCADE,task TEXT NOT NULL,done_definition TEXT NOT NULL DEFAULT '',warning TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'todo' CHECK(state IN('todo','in_progress','blocked','done')),cli_tool TEXT NOT NULL,position INTEGER NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,started_at TEXT,finished_at TEXT,UNIQUE(lane_id,position));
CREATE TABLE IF NOT EXISTS job_runs(id INTEGER PRIMARY KEY,job_id INTEGER NOT NULL REFERENCES jobs ON DELETE CASCADE,attempt INTEGER NOT NULL,tmux_session TEXT NOT NULL,status TEXT NOT NULL,exit_code INTEGER,started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,ended_at TEXT,result_summary TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS job_events(id INTEGER PRIMARY KEY,job_run_id INTEGER NOT NULL REFERENCES job_runs ON DELETE CASCADE,sequence INTEGER NOT NULL,kind TEXT NOT NULL,content TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(job_run_id,sequence));
CREATE TABLE IF NOT EXISTS notifications(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,job_id INTEGER REFERENCES jobs ON DELETE CASCADE,job_run_id INTEGER REFERENCES job_runs ON DELETE CASCADE,kind TEXT NOT NULL CHECK(kind IN('done','error')),title TEXT NOT NULL,read INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(job_run_id,kind));
CREATE INDEX IF NOT EXISTS notifications_user_id ON notifications(user_id,id DESC);`)
	if e != nil {
		return e
	}
	_, _ = a.DB.Exec("ALTER TABLE jobs ADD COLUMN pending_comment TEXT NOT NULL DEFAULT ''")
	_, e = a.DB.Exec(`CREATE TABLE IF NOT EXISTS workspaces(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,root TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,root));
CREATE TABLE IF NOT EXISTS projects(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,workspace_id INTEGER NOT NULL REFERENCES workspaces ON DELETE CASCADE,name TEXT NOT NULL,directory TEXT NOT NULL,worktree_path TEXT,worktree_branch TEXT,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(workspace_id,directory));
CREATE TABLE IF NOT EXISTS custom_cli_tools(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,command TEXT NOT NULL DEFAULT '',argv_json TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,name));
CREATE TABLE IF NOT EXISTS boards(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,workspace_id INTEGER NOT NULL UNIQUE REFERENCES workspaces ON DELETE RESTRICT,name TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS columns(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,board_id INTEGER NOT NULL REFERENCES boards ON DELETE CASCADE,lane_id INTEGER UNIQUE REFERENCES lanes ON DELETE RESTRICT,name TEXT NOT NULL,position INTEGER NOT NULL,paused INTEGER NOT NULL DEFAULT 0,worktree_enabled INTEGER NOT NULL DEFAULT 0,worktree_name TEXT,worktree_path TEXT,CHECK((worktree_enabled=0 AND worktree_name IS NULL AND worktree_path IS NULL) OR (worktree_enabled=1 AND worktree_name IS NOT NULL AND worktree_path IS NOT NULL)),UNIQUE(board_id,position));`)
	if e == nil {
		e = a.migrateCardinality()
	}
	if e == nil {
		a.DB.Exec(`ALTER TABLE user_settings ADD COLUMN hermes_url TEXT NOT NULL DEFAULT ''`)
		a.DB.Exec(`ALTER TABLE user_settings ADD COLUMN hermes_api_key TEXT NOT NULL DEFAULT ''`)
		a.DB.Exec(`ALTER TABLE user_settings ADD COLUMN hermes_model TEXT NOT NULL DEFAULT 'hermes-agent'`)
		a.DB.Exec(`PRAGMA writable_schema=ON`)
		a.DB.Exec(`UPDATE sqlite_master SET sql=replace(sql,"CHECK(default_cli IN('codex','claude'))",'') WHERE name='user_settings'`)
		a.DB.Exec(`UPDATE sqlite_master SET sql=replace(sql,"CHECK(cli_tool IN('codex','claude'))",'') WHERE name='jobs'`)
		a.DB.Exec(`PRAGMA writable_schema=OFF`)
		a.DB.Exec(`ALTER TABLE custom_cli_tools ADD COLUMN command TEXT NOT NULL DEFAULT ''`)
		a.DB.Exec(`ALTER TABLE columns ADD COLUMN lane_id INTEGER REFERENCES lanes ON DELETE RESTRICT`)
		a.DB.Exec(`ALTER TABLE columns ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`)
		a.DB.Exec(`ALTER TABLE columns ADD COLUMN project_id INTEGER REFERENCES projects ON DELETE RESTRICT`)
		_, e = a.DB.Exec(`CREATE TABLE IF NOT EXISTS workspace_members(workspace_id INTEGER NOT NULL REFERENCES workspaces ON DELETE CASCADE,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,role TEXT NOT NULL CHECK(role IN('owner','member')),created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(workspace_id,user_id)); CREATE TABLE IF NOT EXISTS workspace_invitations(id INTEGER PRIMARY KEY,workspace_id INTEGER NOT NULL REFERENCES workspaces ON DELETE CASCADE,email TEXT NOT NULL,token_hash TEXT UNIQUE NOT NULL,invited_by INTEGER NOT NULL REFERENCES users,expires_at TEXT NOT NULL,accepted_at TEXT,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP); CREATE UNIQUE INDEX IF NOT EXISTS active_workspace_invitation ON workspace_invitations(workspace_id,email) WHERE accepted_at IS NULL;`)
		a.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS columns_lane_id ON columns(lane_id)`)
		a.DB.Exec(`UPDATE columns SET lane_id=id WHERE lane_id IS NULL AND EXISTS(SELECT 1 FROM lanes WHERE lanes.id=columns.id)`)
		_, e = a.DB.Exec(`INSERT INTO workspaces(user_id,name,root) SELECT s.user_id,'Default',s.workspace_root FROM user_settings s WHERE NOT EXISTS(SELECT 1 FROM workspaces w WHERE w.user_id=s.user_id AND w.root=s.workspace_root AND w.root<>'')`)
		if e == nil {
			_, e = a.DB.Exec(`INSERT OR IGNORE INTO workspace_members(workspace_id,user_id,role) SELECT id,user_id,'owner' FROM workspaces; INSERT OR IGNORE INTO projects(user_id,workspace_id,name,directory) SELECT user_id,id,'Default Project',root FROM workspaces WHERE root<>''; UPDATE columns SET project_id=(SELECT p.id FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.id=columns.board_id ORDER BY p.id LIMIT 1) WHERE project_id IS NULL;`)
		}
	}
	return e
}
func (a *App) migrateCardinality() (err error) {
	conn, err := a.DB.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if _, e := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err == nil {
			err = e
		}
	}()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ws, bs string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspaces'`).Scan(&ws); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='boards'`).Scan(&bs); err != nil {
		return err
	}
	statements := []string{`DROP TABLE IF EXISTS workspaces_new`, `DROP TABLE IF EXISTS boards_new`}
	if strings.Contains(strings.ReplaceAll(ws, " ", ""), "UNIQUE(user_id,root)") {
		statements = append(statements, `CREATE TABLE workspaces_new(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,root TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`, `INSERT INTO workspaces_new SELECT * FROM workspaces`, `DROP TABLE workspaces`, `ALTER TABLE workspaces_new RENAME TO workspaces`)
	}
	if strings.Contains(strings.ReplaceAll(bs, " ", ""), "workspace_idINTEGERNOTNULLUNIQUE") {
		statements = append(statements, `CREATE TABLE boards_new(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,workspace_id INTEGER NOT NULL REFERENCES workspaces ON DELETE RESTRICT,name TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`, `INSERT INTO boards_new SELECT * FROM boards`, `DROP TABLE boards`, `ALTER TABLE boards_new RENAME TO boards`)
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	var broken sql.NullString
	if err = tx.QueryRow(`SELECT group_concat("table"||':'||rowid||':'||parent) FROM pragma_foreign_key_check`).Scan(&broken); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if broken.Valid {
		return fmt.Errorf("foreign key check failed: %s", broken.String)
	}
	return tx.Commit()
}

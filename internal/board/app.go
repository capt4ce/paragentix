package board

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed web/*
var web embed.FS

type App struct {
	DB        *sql.DB
	Workspace string
	secure    bool
	wake      chan struct{}
	stop      chan struct{}
	wg        sync.WaitGroup
}
type ctxKey struct{}
type Job struct {
	ID       int64  `json:"id"`
	LaneID   int64  `json:"lane_id"`
	Task     string `json:"task"`
	Done     string `json:"done_definition"`
	Warning  string `json:"warning"`
	State    string `json:"state"`
	CLI      string `json:"cli_tool"`
	Position int    `json:"position"`
	Attempts int    `json:"attempt_count"`
	Created  string `json:"created_at"`
	Updated  string `json:"updated_at"`
}
type Lane struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	Paused   bool   `json:"paused"`
	Jobs     []Job  `json:"jobs"`
}

func Open(path, workspace string) (*App, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	a := &App{DB: db, Workspace: workspace, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	if e = a.migrate(); e != nil {
		db.Close()
		return nil, e
	}
	a.reconcile()
	a.wg.Add(1)
	go a.scheduler()
	return a, nil
}
func (a *App) Close() { close(a.stop); a.wg.Wait(); a.DB.Close() }
func (a *App) migrate() error {
	_, e := a.DB.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,email TEXT UNIQUE NOT NULL,password_hash BLOB NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS auth_sessions(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,token_hash TEXT UNIQUE NOT NULL,expires_at TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS user_settings(user_id INTEGER PRIMARY KEY REFERENCES users ON DELETE CASCADE,default_cli TEXT NOT NULL DEFAULT 'codex' CHECK(default_cli IN('codex','claude')),workspace_root TEXT NOT NULL,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS lanes(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,position INTEGER NOT NULL,paused INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,position));
CREATE TABLE IF NOT EXISTS jobs(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,lane_id INTEGER NOT NULL REFERENCES lanes ON DELETE CASCADE,task TEXT NOT NULL,done_definition TEXT NOT NULL DEFAULT '',warning TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'todo' CHECK(state IN('todo','in_progress','blocked','done')),cli_tool TEXT NOT NULL CHECK(cli_tool IN('codex','claude')),position INTEGER NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,started_at TEXT,finished_at TEXT,UNIQUE(lane_id,position));
CREATE TABLE IF NOT EXISTS job_runs(id INTEGER PRIMARY KEY,job_id INTEGER NOT NULL REFERENCES jobs ON DELETE CASCADE,attempt INTEGER NOT NULL,tmux_session TEXT NOT NULL,status TEXT NOT NULL,exit_code INTEGER,started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,ended_at TEXT,result_summary TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS job_events(id INTEGER PRIMARY KEY,job_run_id INTEGER NOT NULL REFERENCES job_runs ON DELETE CASCADE,sequence INTEGER NOT NULL,kind TEXT NOT NULL,content TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(job_run_id,sequence));`)
	if e != nil {
		return e
	}
	_, e = a.DB.Exec(`CREATE TABLE IF NOT EXISTS workspaces(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,root TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,root));
CREATE TABLE IF NOT EXISTS projects(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,workspace_id INTEGER NOT NULL REFERENCES workspaces ON DELETE CASCADE,name TEXT NOT NULL,directory TEXT NOT NULL,worktree_path TEXT,worktree_branch TEXT,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(workspace_id,directory));
CREATE TABLE IF NOT EXISTS custom_cli_tools(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,argv_json TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,name));`)
	if e == nil {
		_, e = a.DB.Exec(`INSERT OR IGNORE INTO workspaces(user_id,name,root) SELECT user_id,'Default',workspace_root FROM user_settings`)
	}
	return e
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) {
	jsonOut(w, status, map[string]string{"error": s})
}
func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}
func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("session")
		if e != nil {
			fail(w, 401, "authentication required")
			return
		}
		var uid int64
		e = a.DB.QueryRow(`SELECT user_id FROM auth_sessions WHERE token_hash=? AND expires_at>datetime('now')`, hash(c.Value)).Scan(&uid)
		if e != nil {
			fail(w, 401, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, uid)))
	})
}
func uid(r *http.Request) int64 { return r.Context().Value(ctxKey{}).(int64) }
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/auth/signup", a.signup)
	m.HandleFunc("POST /api/auth/login", a.login)
	m.Handle("POST /api/auth/logout", a.auth(http.HandlerFunc(a.logout)))
	m.Handle("GET /api/auth/me", a.auth(http.HandlerFunc(a.me)))
	m.Handle("/api/lanes", a.auth(http.HandlerFunc(a.lanes)))
	m.Handle("/api/lanes/", a.auth(http.HandlerFunc(a.lanePath)))
	m.Handle("/api/jobs/", a.auth(http.HandlerFunc(a.jobPath)))
	m.Handle("/api/settings", a.auth(http.HandlerFunc(a.settings)))
	m.Handle("/api/cli-tools", a.auth(http.HandlerFunc(a.tools)))
	m.Handle("/api/workspaces", a.auth(http.HandlerFunc(a.workspaces)))
	m.Handle("/api/projects", a.auth(http.HandlerFunc(a.projects)))
	m.Handle("/api/projects/", a.auth(http.HandlerFunc(a.projectPath)))
	m.Handle("/api/boards", a.auth(http.HandlerFunc(a.boards)))
	sub, _ := fs.Sub(web, "web")
	files := http.FileServer(http.FS(sub))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "not found")
			return
		}
		if _, e := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); e != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
	return security(m)
}
func security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
func (a *App) signup(w http.ResponseWriter, r *http.Request) {
	var x struct{ Email, Password string }
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	x.Email = strings.ToLower(strings.TrimSpace(x.Email))
	if !strings.Contains(x.Email, "@") || len(x.Password) < 8 {
		fail(w, 400, "valid email and 8 character password required")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(x.Password), bcrypt.DefaultCost)
	tx, _ := a.DB.Begin()
	res, e := tx.Exec("INSERT INTO users(email,password_hash) VALUES(?,?)", x.Email, h)
	if e != nil {
		tx.Rollback()
		fail(w, 409, "account unavailable")
		return
	}
	id, _ := res.LastInsertId()
	tx.Exec("INSERT INTO user_settings(user_id,workspace_root) VALUES(?,?)", id, a.Workspace)
	tx.Exec("INSERT INTO workspaces(user_id,name,root) VALUES(?,'Default',?)", id, a.Workspace)
	tx.Exec("INSERT INTO lanes(user_id,name,position) VALUES(?,'Lane 1',0)", id)
	tx.Commit()
	a.newSession(w, id)
	jsonOut(w, 201, map[string]any{"id": id, "email": x.Email})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var x struct{ Email, Password string }
	if decode(r, &x) != nil {
		fail(w, 401, "invalid email or password")
		return
	}
	var id int64
	var h []byte
	e := a.DB.QueryRow("SELECT id,password_hash FROM users WHERE email=?", strings.ToLower(strings.TrimSpace(x.Email))).Scan(&id, &h)
	if e != nil || bcrypt.CompareHashAndPassword(h, []byte(x.Password)) != nil {
		time.Sleep(100 * time.Millisecond)
		fail(w, 401, "invalid email or password")
		return
	}
	a.newSession(w, id)
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) newSession(w http.ResponseWriter, id int64) {
	t := token()
	a.DB.Exec("INSERT INTO auth_sessions(user_id,token_hash,expires_at) VALUES(?,?,datetime('now','+30 days'))", id, hash(t))
	http.SetCookie(w, &http.Cookie{Name: "session", Value: t, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode, MaxAge: 2592000})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("session"); e == nil {
		a.DB.Exec("DELETE FROM auth_sessions WHERE token_hash=?", hash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(204)
}
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	var email string
	a.DB.QueryRow("SELECT email FROM users WHERE id=?", uid(r)).Scan(&email)
	jsonOut(w, 200, map[string]any{"id": uid(r), "email": email})
}
func (a *App) lanes(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var x struct{ Name string }
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
			fail(w, 400, "name required")
			return
		}
		var p int
		a.DB.QueryRow("SELECT COALESCE(MAX(position)+1,0) FROM lanes WHERE user_id=?", uid(r)).Scan(&p)
		res, e := a.DB.Exec("INSERT INTO lanes(user_id,name,position) VALUES(?,?,?)", uid(r), strings.TrimSpace(x.Name), p)
		if e != nil {
			fail(w, 500, "could not create lane")
			return
		}
		id, _ := res.LastInsertId()
		jsonOut(w, 201, map[string]any{"id": id})
		return
	}
	rows, _ := a.DB.Query("SELECT id,name,position,paused FROM lanes WHERE user_id=? ORDER BY position", uid(r))
	defer rows.Close()
	out := []Lane{}
	for rows.Next() {
		l := Lane{Jobs: []Job{}}
		rows.Scan(&l.ID, &l.Name, &l.Position, &l.Paused)
		jr, _ := a.DB.Query("SELECT id,lane_id,task,done_definition,warning,state,cli_tool,position,attempt_count,created_at,updated_at FROM jobs WHERE lane_id=? ORDER BY CASE state WHEN 'in_progress' THEN 0 WHEN 'blocked' THEN 1 WHEN 'todo' THEN 2 ELSE 3 END,position", l.ID)
		for jr.Next() {
			var j Job
			jr.Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.CLI, &j.Position, &j.Attempts, &j.Created, &j.Updated)
			l.Jobs = append(l.Jobs, j)
		}
		jr.Close()
		out = append(out, l)
	}
	jsonOut(w, 200, out)
}
func pathID(s string) (int64, error) {
	return strconv.ParseInt(strings.Split(strings.Trim(s, "/"), "/")[0], 10, 64)
}
func (a *App) lanePath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/lanes/")
	id, e := pathID(rest)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var own int
	e = a.DB.QueryRow("SELECT 1 FROM lanes WHERE id=? AND user_id=?", id, uid(r)).Scan(&own)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	if strings.HasSuffix(rest, "/jobs") && r.Method == "POST" {
		a.createJob(w, r, id)
		return
	}
	switch r.Method {
	case "PATCH":
		var x struct {
			Name   *string
			Paused *bool
		}
		if decode(r, &x) != nil {
			fail(w, 400, "invalid request")
			return
		}
		if x.Name != nil {
			n := strings.TrimSpace(*x.Name)
			if n == "" {
				fail(w, 400, "name required")
				return
			}
			a.DB.Exec("UPDATE lanes SET name=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", n, id)
		}
		if x.Paused != nil {
			a.DB.Exec("UPDATE lanes SET paused=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", *x.Paused, id)
		}
		a.signal()
		jsonOut(w, 200, map[string]bool{"ok": true})
	case "DELETE":
		var n int
		a.DB.QueryRow("SELECT count(*) FROM jobs WHERE lane_id=?", id).Scan(&n)
		if n > 0 {
			fail(w, 409, "lane must be empty")
			return
		}
		a.DB.Exec("DELETE FROM lanes WHERE id=?", id)
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}
func (a *App) createJob(w http.ResponseWriter, r *http.Request, lane int64) {
	var x struct{ Task, DoneDefinition string }
	if decode(r, &x) != nil || strings.TrimSpace(x.Task) == "" {
		fail(w, 400, "task required")
		return
	}
	var cli string
	a.DB.QueryRow("SELECT default_cli FROM user_settings WHERE user_id=?", uid(r)).Scan(&cli)
	var p int
	a.DB.QueryRow("SELECT COALESCE(MAX(position)+1,0) FROM jobs WHERE lane_id=?", lane).Scan(&p)
	warning := ""
	if strings.TrimSpace(x.DoneDefinition) == "" {
		warning = "Completion criteria generation deferred: add criteria manually or run the task as-is."
	}
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,done_definition,warning,cli_tool,position) VALUES(?,?,?,?,?,?,?)", uid(r), lane, strings.TrimSpace(x.Task), strings.TrimSpace(x.DoneDefinition), warning, cli, p)
	id, _ := res.LastInsertId()
	jsonOut(w, 201, map[string]any{"id": id, "warning": warning})
	a.signal()
}
func (a *App) jobPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	id, e := pathID(rest)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var state string
	e = a.DB.QueryRow("SELECT state FROM jobs WHERE id=? AND user_id=?", id, uid(r)).Scan(&state)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 {
		if r.Method == "GET" {
			a.jobDetail(w, id)
			return
		}
		if r.Method == "PATCH" {
			a.editJob(w, r, id, state)
			return
		}
	} else {
		switch parts[1] {
		case "reorder":
			a.reorder(w, r, id, state)
			return
		case "events":
			a.events(w, id)
			return
		case "stream":
			a.stream(w, r, id)
			return
		case "retry", "cancel", "approve", "input":
			a.action(w, r, id, state, parts[1])
			return
		}
	}
	fail(w, 405, "method not allowed")
}
func (a *App) jobDetail(w http.ResponseWriter, id int64) {
	var j Job
	a.DB.QueryRow("SELECT id,lane_id,task,done_definition,warning,state,cli_tool,position,attempt_count,created_at,updated_at FROM jobs WHERE id=?", id).Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.CLI, &j.Position, &j.Attempts, &j.Created, &j.Updated)
	var ev []map[string]any
	rows, _ := a.DB.Query("SELECT e.id,e.kind,e.content,e.created_at FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? ORDER BY e.id", id)
	defer rows.Close()
	for rows.Next() {
		var i int
		var k, c, t string
		rows.Scan(&i, &k, &c, &t)
		ev = append(ev, map[string]any{"id": i, "kind": k, "content": c, "created_at": t})
	}
	jsonOut(w, 200, map[string]any{"job": j, "events": ev})
}
func (a *App) editJob(w http.ResponseWriter, r *http.Request, id int64, state string) {
	var x struct{ Task, DoneDefinition *string }
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	if state == "done" || (state != "todo" && x.Task != nil) {
		fail(w, 409, "field is locked in this state")
		return
	}
	if x.Task != nil && strings.TrimSpace(*x.Task) == "" {
		fail(w, 400, "task required")
		return
	}
	if x.Task != nil {
		a.DB.Exec("UPDATE jobs SET task=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", strings.TrimSpace(*x.Task), id)
	}
	if x.DoneDefinition != nil {
		a.DB.Exec("UPDATE jobs SET done_definition=?,warning='',updated_at=CURRENT_TIMESTAMP WHERE id=?", strings.TrimSpace(*x.DoneDefinition), id)
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) reorder(w http.ResponseWriter, r *http.Request, id int64, state string) {
	if state != "todo" {
		fail(w, 409, "only todo jobs can reorder")
		return
	}
	var x struct{ Position int }
	if decode(r, &x) != nil || x.Position < 0 {
		fail(w, 400, "invalid position")
		return
	}
	var lane int64
	a.DB.QueryRow("SELECT lane_id FROM jobs WHERE id=?", id).Scan(&lane)
	tx, _ := a.DB.Begin()
	rows, _ := tx.Query("SELECT id FROM jobs WHERE lane_id=? AND state='todo' AND id<>? ORDER BY position", lane, id)
	ids := []int64{}
	for rows.Next() {
		var n int64
		rows.Scan(&n)
		ids = append(ids, n)
	}
	rows.Close()
	if x.Position > len(ids) {
		x.Position = len(ids)
	}
	ids = append(ids, 0)
	copy(ids[x.Position+1:], ids[x.Position:])
	ids[x.Position] = id
	for i, n := range ids {
		tx.Exec("UPDATE jobs SET position=? WHERE id=?", -i-1, n)
	}
	for i, n := range ids {
		tx.Exec("UPDATE jobs SET position=? WHERE id=?", i, n)
	}
	tx.Commit()
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) action(w http.ResponseWriter, r *http.Request, id int64, state, act string) {
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	if act == "cancel" {
		if state != "blocked" && state != "in_progress" {
			fail(w, 409, "cannot cancel")
			return
		}
		a.DB.Exec("UPDATE jobs SET state='todo',updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
		exec.Command("tmux", "kill-session", "-t", fmt.Sprintf("agent-job-%d", id)).Run()
	} else {
		if state != "blocked" {
			fail(w, 409, "job is not blocked")
			return
		}
		if act == "input" || act == "approve" {
			var x struct{ Input string }
			decode(r, &x)
			text := x.Input
			if act == "approve" {
				text = "y"
			}
			if text == "" {
				fail(w, 400, "input required")
				return
			}
			exec.Command("tmux", "send-keys", "-t", fmt.Sprintf("agent-job-%d", id), "-l", text).Run()
			exec.Command("tmux", "send-keys", "-t", fmt.Sprintf("agent-job-%d", id), "Enter").Run()
		}
		a.DB.Exec("UPDATE jobs SET state='todo',updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
	a.signal()
}
func (a *App) events(w http.ResponseWriter, id int64) {
	rows, _ := a.DB.Query("SELECT e.id,e.kind,e.content,e.created_at FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? ORDER BY e.id", id)
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var i int
		var k, c, t string
		rows.Scan(&i, &k, &c, &t)
		out = append(out, map[string]any{"id": i, "kind": k, "content": c, "created_at": t})
	}
	jsonOut(w, 200, out)
}
func (a *App) stream(w http.ResponseWriter, r *http.Request, id int64) {
	f, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	last := int64(0)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		rows, _ := a.DB.Query("SELECT e.id,e.kind,e.content FROM job_events e JOIN job_runs x ON x.id=e.job_run_id WHERE x.job_id=? AND e.id>? ORDER BY e.id", id, last)
		for rows.Next() {
			var kind, content string
			rows.Scan(&last, &kind, &content)
			b, _ := json.Marshal(map[string]any{"id": last, "kind": kind, "content": content})
			fmt.Fprintf(w, "data: %s\n\n", b)
			f.Flush()
		}
		rows.Close()
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}
func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PATCH" {
		var x struct {
			DefaultCLI string `json:"default_cli"`
		}
		if decode(r, &x) != nil || (x.DefaultCLI != "codex" && x.DefaultCLI != "claude") {
			fail(w, 400, "invalid CLI")
			return
		}
		if !available(x.DefaultCLI) {
			fail(w, 409, "CLI is unavailable")
			return
		}
		a.DB.Exec("UPDATE user_settings SET default_cli=?,updated_at=CURRENT_TIMESTAMP WHERE user_id=?", x.DefaultCLI, uid(r))
	}
	var cli, root string
	a.DB.QueryRow("SELECT default_cli,workspace_root FROM user_settings WHERE user_id=?", uid(r)).Scan(&cli, &root)
	jsonOut(w, 200, map[string]string{"default_cli": cli, "workspace_root": root})
}
func available(name string) bool { _, e := exec.LookPath(name); return e == nil }
func (a *App) workspaces(w http.ResponseWriter, r *http.Request) {
	rows, _ := a.DB.Query("SELECT id,name,root FROM workspaces WHERE user_id=? ORDER BY id", uid(r))
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, root string
		rows.Scan(&id, &name, &root)
		out = append(out, map[string]any{"id": id, "name": name, "root": root})
	}
	jsonOut(w, 200, out)
}
func safeProjectDir(root, directory string) (string, bool) {
	root, _ = filepath.Abs(root)
	path, e := filepath.Abs(filepath.Join(root, directory))
	return path, e == nil && path != root && strings.HasPrefix(path, root+string(os.PathSeparator))
}
func (a *App) projects(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	var x struct {
		Name, Directory string
		WorkspaceID     int64 `json:"workspace_id"`
	}
	if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
		fail(w, 400, "name and directory required")
		return
	}
	var wid int64
	var root string
	if x.WorkspaceID == 0 {
		a.DB.QueryRow("SELECT id,root FROM workspaces WHERE user_id=? ORDER BY id LIMIT 1", uid(r)).Scan(&wid, &root)
	} else {
		wid = x.WorkspaceID
		a.DB.QueryRow("SELECT root FROM workspaces WHERE id=? AND user_id=?", wid, uid(r)).Scan(&root)
	}
	path, ok := safeProjectDir(root, x.Directory)
	info, e := os.Stat(path)
	if !ok || e != nil || !info.IsDir() {
		fail(w, 400, "directory must be an existing path inside workspace")
		return
	}
	res, e := a.DB.Exec("INSERT INTO projects(user_id,workspace_id,name,directory) VALUES(?,?,?,?)", uid(r), wid, strings.TrimSpace(x.Name), path)
	if e != nil {
		fail(w, 409, "project unavailable")
		return
	}
	id, _ := res.LastInsertId()
	jsonOut(w, 201, map[string]any{"id": id, "directory": path})
}
func (a *App) projectPath(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(strings.TrimPrefix(r.URL.Path, "/api/projects/"))
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var name, directory string
	e = a.DB.QueryRow("SELECT name,directory FROM projects WHERE id=? AND user_id=?", id, uid(r)).Scan(&name, &directory)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "name": name, "directory": directory})
}
func (a *App) boards(w http.ResponseWriter, r *http.Request) {
	rw := httptestResponse{ResponseWriter: w}
	a.lanes(&rw, r)
	if rw.status != 200 {
		return
	}
	var lanes []Lane
	json.Unmarshal([]byte(rw.body.String()), &lanes)
	jsonOut(w, 200, map[string]any{"columns": lanes})
}

type httptestResponse struct {
	http.ResponseWriter
	body   strings.Builder
	status int
}

func (w *httptestResponse) Header() http.Header         { return w.ResponseWriter.Header() }
func (w *httptestResponse) WriteHeader(n int)           { w.status = n }
func (w *httptestResponse) Write(b []byte) (int, error) { return w.body.Write(b) }
func (a *App) tools(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var x struct {
			Name string
			Argv []string
		}
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" || len(x.Argv) == 0 || strings.TrimSpace(x.Argv[0]) == "" {
			fail(w, 400, "name and argv required")
			return
		}
		b, _ := json.Marshal(x.Argv)
		res, e := a.DB.Exec("INSERT INTO custom_cli_tools(user_id,name,argv_json) VALUES(?,?,?)", uid(r), strings.TrimSpace(x.Name), b)
		if e != nil {
			fail(w, 409, "tool unavailable")
			return
		}
		id, _ := res.LastInsertId()
		jsonOut(w, 201, map[string]any{"id": id})
		return
	}
	out := []map[string]any{{"id": "codex", "name": "Codex", "available": available("codex"), "reason": reason("codex")}, {"id": "claude", "name": "Claude Code", "available": available("claude"), "reason": reason("claude")}}
	rows, _ := a.DB.Query("SELECT id,name,argv_json FROM custom_cli_tools WHERE user_id=?", uid(r))
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, s string
		rows.Scan(&id, &name, &s)
		var argv []string
		json.Unmarshal([]byte(s), &argv)
		out = append(out, map[string]any{"id": id, "name": name, "argv": argv, "available": available(argv[0]), "reason": reason(argv[0])})
	}
	jsonOut(w, 200, out)
}
func reason(s string) string {
	if available(s) {
		return "Available"
	}
	return "Not installed"
}
func (a *App) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *App) scheduler() {
	defer a.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		a.schedule()
		select {
		case <-a.stop:
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}
func (a *App) schedule() {
	rows, e := a.DB.Query(`SELECT j.id,j.task,j.done_definition,j.cli_tool,s.workspace_root FROM jobs j JOIN lanes l ON l.id=j.lane_id JOIN user_settings s ON s.user_id=j.user_id WHERE j.state='todo' AND l.paused=0 AND NOT EXISTS(SELECT 1 FROM jobs x WHERE x.lane_id=j.lane_id AND x.state IN('in_progress','blocked')) AND j.id=(SELECT id FROM jobs q WHERE q.lane_id=j.lane_id AND q.state='todo' ORDER BY q.position LIMIT 1)`)
	if e != nil {
		return
	}
	type q struct {
		id                    int64
		task, done, cli, root string
	}
	var qs []q
	for rows.Next() {
		var x q
		rows.Scan(&x.id, &x.task, &x.done, &x.cli, &x.root)
		qs = append(qs, x)
	}
	rows.Close()
	for _, x := range qs {
		a.start(x.id, x.task, x.done, x.cli, x.root)
	}
}
func (a *App) start(id int64, task, done, cli, root string) {
	if !available("tmux") || !available(cli) {
		a.DB.Exec("UPDATE jobs SET state='blocked',warning='Selected CLI or tmux is unavailable',updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
		return
	}
	session := fmt.Sprintf("agent-job-%d", id)
	tx, _ := a.DB.Begin()
	res, e := tx.Exec("UPDATE jobs SET state='in_progress',attempt_count=attempt_count+1,started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='todo'", id)
	if e != nil {
		tx.Rollback()
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return
	}
	var attempt int
	tx.QueryRow("SELECT attempt_count FROM jobs WHERE id=?", id).Scan(&attempt)
	rr, _ := tx.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,?,?,'running')", id, attempt, session)
	run, _ := rr.LastInsertId()
	tx.Commit()
	args := []string{"new-session", "-d", "-s", session, "-c", filepath.Clean(root), cli}
	if e := exec.Command("tmux", args...).Run(); e != nil {
		a.block(id, run, e.Error())
		return
	}
	prompt := task + "\n\nDone definition:\n" + done
	exec.Command("tmux", "send-keys", "-t", session, "-l", prompt).Run()
	exec.Command("tmux", "send-keys", "-t", session, "Enter").Run()
	go a.monitor(id, run, session)
}
func (a *App) monitor(job, run int64, session string) {
	seq := 0
	last := ""
	for i := 0; i < 3600; i++ {
		time.Sleep(time.Second)
		out, e := exec.Command("tmux", "capture-pane", "-p", "-t", session, "-S", "-200").Output()
		text := string(out)
		if text != last {
			seq++
			delta := text
			if strings.HasPrefix(text, last) {
				delta = strings.TrimPrefix(text, last)
			}
			a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,?,?,?)", run, seq, "output", delta)
			last = text
		}
		if e != nil {
			a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", last, run)
			a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='in_progress'", job)
			a.signal()
			return
		}
	}
	a.block(job, run, "execution timed out")
}
func (a *App) block(job, run int64, msg string) {
	a.DB.Exec("UPDATE job_runs SET status='blocked',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", msg, run)
	a.DB.Exec("INSERT OR IGNORE INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'error',?)", run, msg)
	a.DB.Exec("UPDATE jobs SET state='blocked',warning=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", msg, job)
}
func (a *App) reconcile() {
	rows, _ := a.DB.Query("SELECT id FROM jobs WHERE state='in_progress'")
	defer rows.Close()
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		if exec.Command("tmux", "has-session", "-t", fmt.Sprintf("agent-job-%d", id)).Run() != nil {
			a.DB.Exec("UPDATE jobs SET state='blocked',warning='Execution session missing after server restart' WHERE id=?", id)
		}
	}
}

var _ = errors.New
var _ = os.Getenv

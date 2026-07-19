package board

import (
	"database/sql"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"sync"

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
	Mailer    Mailer
	BaseURL   string
}
type ctxKey struct{}
type Job struct {
	ID       int64  `json:"id"`
	LaneID   int64  `json:"lane_id"`
	Task     string `json:"task"`
	Done     string `json:"done_definition"`
	Warning  string `json:"warning"`
	State    string `json:"state"`
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
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/auth/signup", a.signup)
	m.HandleFunc("POST /api/auth/login", a.login)
	m.Handle("POST /api/auth/logout", a.auth(http.HandlerFunc(a.logout)))
	m.Handle("GET /api/auth/me", a.auth(http.HandlerFunc(a.me)))
	m.Handle("/api/lanes", a.auth(http.HandlerFunc(a.lanes)))
	m.Handle("/api/lanes/", a.auth(http.HandlerFunc(a.lanePath)))
	m.Handle("/api/jobs/", a.auth(http.HandlerFunc(a.jobPath)))
	m.Handle("/api/notifications", a.auth(http.HandlerFunc(a.notifications)))
	m.Handle("/api/notifications/", a.auth(http.HandlerFunc(a.notificationPath)))
	m.Handle("/api/settings", a.auth(http.HandlerFunc(a.settings)))
	m.Handle("/api/cli-tools", a.auth(http.HandlerFunc(a.tools)))
	m.Handle("/api/workspaces", a.auth(http.HandlerFunc(a.workspaces)))
	m.Handle("/api/workspaces/", a.auth(http.HandlerFunc(a.workspacePath)))
	m.Handle("/api/projects", a.auth(http.HandlerFunc(a.projects)))
	m.Handle("/api/projects/", a.auth(http.HandlerFunc(a.projectPath)))
	m.Handle("/api/boards", a.auth(http.HandlerFunc(a.boards)))
	m.Handle("/api/boards/", a.auth(http.HandlerFunc(a.boardPath)))
	m.Handle("/api/columns/", a.auth(http.HandlerFunc(a.columnPath)))
	m.HandleFunc("GET /api/invitations/{token}", a.invitationPreview)
	m.Handle("POST /api/invitations/{token}", a.auth(http.HandlerFunc(a.invitationAccept)))
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

package board

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var worktreeNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var columnWords = []string{"amber", "brisk", "cedar", "delta", "ember", "frost", "green", "harbor", "ivory", "jolly", "lunar", "maple"}

func canonicalDir(root, path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("projectDirectory required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	if !contained(rr, real) {
		return "", "", fmt.Errorf("project directory must be inside workspace root")
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("project directory must exist and be a directory")
	}
	f, err := os.Open(real)
	if err != nil {
		return "", "", fmt.Errorf("project directory is unreadable")
	}
	_, err = f.Readdirnames(1)
	f.Close()
	if err != nil && err.Error() != "EOF" {
		return "", "", fmt.Errorf("project directory is unreadable")
	}
	return abs, real, nil
}
func (a *App) workspaceRoot() string {
	if x := os.Getenv("WORKSPACE_ROOT"); x != "" {
		return x
	}
	return a.Workspace
}
func workspaceJSON(id int64, name, dir, real string) map[string]any {
	return map[string]any{"id": id, "name": name, "projectDirectory": dir, "projectDirectoryReal": real}
}

func (a *App) workspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, e := a.DB.Query(`SELECT id,name,root FROM workspaces WHERE user_id=? ORDER BY name`, uid(r))
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var n, d string
			rows.Scan(&id, &n, &d)
			real, _ := filepath.EvalSymlinks(d)
			out = append(out, workspaceJSON(id, n, d, real))
		}
		jsonOut(w, 200, out)
	case "POST":
		var x struct {
			Name             string `json:"name"`
			ProjectDirectory string `json:"projectDirectory"`
		}
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
			fail(w, 400, "name and projectDirectory required")
			return
		}
		d, real, e := canonicalDir(a.workspaceRoot(), x.ProjectDirectory)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		res, e := a.DB.Exec(`INSERT INTO workspaces(user_id,name,root) VALUES(?,?,?)`, uid(r), strings.TrimSpace(x.Name), d)
		if e != nil {
			fail(w, 409, "workspace name or directory unavailable")
			return
		}
		id, _ := res.LastInsertId()
		jsonOut(w, 201, workspaceJSON(id, strings.TrimSpace(x.Name), d, real))
	default:
		fail(w, 405, "method not allowed")
	}
}
func (a *App) workspacePath(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(strings.TrimPrefix(r.URL.Path, "/api/workspaces/"))
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var name, dir string
	if a.DB.QueryRow(`SELECT name,root FROM workspaces WHERE id=? AND user_id=?`, id, uid(r)).Scan(&name, &dir) != nil {
		fail(w, 404, "not found")
		return
	}
	switch r.Method {
	case "GET":
		real, _ := filepath.EvalSymlinks(dir)
		jsonOut(w, 200, workspaceJSON(id, name, dir, real))
	case "PATCH":
		var x struct {
			Name             *string `json:"name"`
			ProjectDirectory *string `json:"projectDirectory"`
		}
		if decode(r, &x) != nil {
			fail(w, 400, "invalid request")
			return
		}
		if x.Name != nil {
			name = strings.TrimSpace(*x.Name)
			if name == "" {
				fail(w, 400, "name required")
				return
			}
		}
		if x.ProjectDirectory != nil {
			var er error
			dir, _, er = canonicalDir(a.workspaceRoot(), *x.ProjectDirectory)
			if er != nil {
				fail(w, 400, er.Error())
				return
			}
		}
		if _, e = a.DB.Exec(`UPDATE workspaces SET name=?,root=? WHERE id=? AND user_id=?`, name, dir, id, uid(r)); e != nil {
			fail(w, 409, "workspace unavailable")
			return
		}
		real, _ := filepath.EvalSymlinks(dir)
		jsonOut(w, 200, workspaceJSON(id, name, dir, real))
	case "DELETE":
		var n int
		a.DB.QueryRow(`SELECT count(*) FROM boards WHERE workspace_id=?`, id).Scan(&n)
		if n > 0 {
			fail(w, 409, "workspace has a board")
			return
		}
		a.DB.Exec(`DELETE FROM workspaces WHERE id=? AND user_id=?`, id, uid(r))
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}

func (a *App) boards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, _ := a.DB.Query(`SELECT b.id,b.name,b.workspace_id,w.name,w.root FROM boards b JOIN workspaces w ON w.id=b.workspace_id WHERE b.user_id=? ORDER BY b.id`, uid(r))
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, wid int64
			var n, wn, d string
			rows.Scan(&id, &n, &wid, &wn, &d)
			out = append(out, map[string]any{"id": id, "name": n, "workspaceId": wid, "workspaceName": wn, "projectDirectory": d})
		}
		jsonOut(w, 200, out)
	case "POST":
		var x struct {
			Name        string `json:"name"`
			WorkspaceID int64  `json:"workspaceId"`
		}
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" || x.WorkspaceID == 0 {
			fail(w, 400, "name and workspaceId required")
			return
		}
		var ok int
		if a.DB.QueryRow(`SELECT 1 FROM workspaces WHERE id=? AND user_id=?`, x.WorkspaceID, uid(r)).Scan(&ok) != nil {
			fail(w, 404, "workspace not found")
			return
		}
		res, e := a.DB.Exec(`INSERT INTO boards(user_id,workspace_id,name) VALUES(?,?,?)`, uid(r), x.WorkspaceID, strings.TrimSpace(x.Name))
		if e != nil {
			fail(w, 409, "workspace already has a board")
			return
		}
		id, _ := res.LastInsertId()
		jsonOut(w, 201, map[string]any{"id": id, "name": strings.TrimSpace(x.Name), "workspaceId": x.WorkspaceID})
	default:
		fail(w, 405, "method not allowed")
	}
}
func (a *App) boardPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/boards/")
	id, e := pathID(rest)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var ok int
	if a.DB.QueryRow(`SELECT 1 FROM boards WHERE id=? AND user_id=?`, id, uid(r)).Scan(&ok) != nil {
		fail(w, 404, "not found")
		return
	}
	if strings.HasSuffix(rest, "/columns") {
		a.columns(w, r, id)
		return
	}
	switch r.Method {
	case "DELETE":
		var n int
		a.DB.QueryRow(`SELECT count(*) FROM columns WHERE board_id=?`, id).Scan(&n)
		if n > 0 {
			fail(w, 409, "board has columns")
			return
		}
		a.DB.Exec(`DELETE FROM boards WHERE id=? AND user_id=?`, id, uid(r))
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}
func generatedColumnName() string {
	var b [2]byte
	rand.Read(b[:])
	return columnWords[int(b[0])%len(columnWords)] + "-" + columnWords[int(b[1])%len(columnWords)]
}
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}
func (a *App) worktreeRoot() string {
	if x := os.Getenv("WORKTREE_ROOT"); x != "" {
		return x
	}
	return filepath.Join(a.workspaceRoot(), ".paragentix-worktrees")
}
func gitOutput(args ...string) error {
	c := exec.Command("git", args...)
	b, e := c.CombinedOutput()
	if e != nil {
		return fmt.Errorf("git: %s", strings.TrimSpace(string(b)))
	}
	return nil
}
func (a *App) columns(w http.ResponseWriter, r *http.Request, board int64) {
	if r.Method == "GET" {
		rows, _ := a.DB.Query(`SELECT id,name,position,paused,worktree_enabled,worktree_name,worktree_path FROM columns WHERE board_id=? AND archived=0 ORDER BY position`, board)
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var n string
			var p int
			var paused, en bool
			var wn, wp sql.NullString
			rows.Scan(&id, &n, &p, &paused, &en, &wn, &wp)
			out = append(out, map[string]any{"id": id, "name": n, "position": p, "paused": paused, "worktreeEnabled": en, "worktreeName": wn.String, "effectiveDirectory": wp.String, "jobs": []Job{}})
		}
		rows.Close()
		for _, c := range out {
			jr, _ := a.DB.Query(`SELECT j.id,j.lane_id,j.task,j.done_definition,j.warning,j.state,j.cli_tool,j.position,j.attempt_count,j.created_at,j.updated_at FROM jobs j JOIN columns c ON c.lane_id=j.lane_id WHERE c.id=? AND c.board_id=? ORDER BY j.position`, c["id"], board)
			jobs := []Job{}
			for jr.Next() {
				var j Job
				jr.Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.CLI, &j.Position, &j.Attempts, &j.Created, &j.Updated)
				jobs = append(jobs, j)
			}
			jr.Close()
			c["jobs"] = jobs
		}
		jsonOut(w, 200, out)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	var x struct {
		Name            string `json:"name"`
		WorktreeEnabled bool   `json:"worktreeEnabled"`
		WorktreeName    string `json:"worktreeName"`
	}
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	x.Name = strings.TrimSpace(x.Name)
	if x.Name == "" {
		x.Name = generatedColumnName()
	}
	var wn, wp any
	if x.WorktreeEnabled {
		name := strings.TrimSpace(x.WorktreeName)
		if name == "" {
			name = slug(x.Name)
		}
		if !worktreeNameRE.MatchString(name) {
			fail(w, 400, "worktreeName must match ^[a-z0-9]+(?:-[a-z0-9]+)*$")
			return
		}
		var project string
		a.DB.QueryRow(`SELECT w.root FROM boards b JOIN workspaces w ON w.id=b.workspace_id WHERE b.id=?`, board).Scan(&project)
		if _, _, e := canonicalDir(a.workspaceRoot(), project); e != nil {
			fail(w, 409, e.Error())
			return
		}
		path := filepath.Join(a.worktreeRoot(), strconv.FormatInt(board, 10), name)
		root, _ := filepath.Abs(a.worktreeRoot())
		path, _ = filepath.Abs(path)
		if !contained(root, path) {
			fail(w, 400, "invalid worktree path")
			return
		}
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			fail(w, 409, "worktree path exists")
			return
		}
		os.MkdirAll(filepath.Dir(path), 0755)
		if e := gitOutput("-C", project, "worktree", "add", "--", path); e != nil {
			os.Remove(path)
			fail(w, 409, e.Error())
			return
		}
		wn, wp = name, path
	}
	var p int
	a.DB.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM columns WHERE board_id=?`, board).Scan(&p)
	tx, _ := a.DB.Begin()
	var lanePosition int
	tx.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM lanes WHERE user_id=?`, uid(r)).Scan(&lanePosition)
	laneRes, e := tx.Exec(`INSERT INTO lanes(user_id,name,position) VALUES(?,?,?)`, uid(r), x.Name, lanePosition)
	if e != nil {
		tx.Rollback()
		if wp != nil {
			gitOutput("worktree", "remove", "--", wp.(string))
		}
		fail(w, 409, "column unavailable")
		return
	}
	laneID, _ := laneRes.LastInsertId()
	res, e := tx.Exec(`INSERT INTO columns(user_id,board_id,lane_id,name,position,worktree_enabled,worktree_name,worktree_path) VALUES(?,?,?,?,?,?,?,?)`, uid(r), board, laneID, x.Name, p, x.WorktreeEnabled, wn, wp)
	if e != nil {
		tx.Rollback()
		if wp != nil {
			gitOutput("worktree", "remove", "--", wp.(string))
		}
		fail(w, 409, "column unavailable")
		return
	}
	id, _ := res.LastInsertId()
	tx.Commit()
	jsonOut(w, 201, map[string]any{"id": id, "name": x.Name, "worktreeEnabled": x.WorktreeEnabled, "worktreeName": wn})
}
func (a *App) columnPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/columns/")
	id, e := pathID(rest)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var wt bool
	var laneID int64
	var path sql.NullString
	if a.DB.QueryRow(`SELECT lane_id,worktree_enabled,worktree_path FROM columns WHERE id=? AND user_id=?`, id, uid(r)).Scan(&laneID, &wt, &path) != nil {
		fail(w, 404, "not found")
		return
	}
	if strings.HasSuffix(rest, "/jobs") && r.Method == "POST" {
		a.createJob(w, r, laneID)
		return
	}
	switch r.Method {
	case "PATCH":
		var x struct {
			Name   *string `json:"name"`
			Paused *bool   `json:"paused"`
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
			a.DB.Exec(`UPDATE columns SET name=? WHERE id=?`, n, id)
			a.DB.Exec(`UPDATE lanes SET name=? WHERE id=?`, n, laneID)
		}
		if x.Paused != nil {
			a.DB.Exec(`UPDATE columns SET paused=? WHERE id=?`, *x.Paused, id)
			a.DB.Exec(`UPDATE lanes SET paused=? WHERE id=?`, *x.Paused, laneID)
		}
		jsonOut(w, 200, map[string]bool{"ok": true})
	case "DELETE":
		a.DB.Exec(`UPDATE columns SET archived=1 WHERE id=? AND user_id=?`, id, uid(r))
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}

func commandSettingsJSON(a *App, user int64) (map[string]string, error) {
	out := map[string]string{"codex": "codex", "claude": "claude"}
	rows, e := a.DB.Query(`SELECT name,command FROM custom_cli_tools WHERE user_id=? AND name IN ('codex','claude')`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var n, c string
		rows.Scan(&n, &c)
		out[n] = c
	}
	return out, nil
}
func storeCommands(a *App, user int64, cmds map[string]string) error {
	for _, tool := range []string{"codex", "claude"} {
		c, ok := cmds[tool]
		if !ok {
			continue
		}
		argv, e := parseCommand(c)
		if e != nil {
			return fmt.Errorf("%s command: %w", tool, e)
		}
		b, _ := json.Marshal(argv)
		_, e = a.DB.Exec(`INSERT INTO custom_cli_tools(user_id,name,command,argv_json) VALUES(?,?,?,?) ON CONFLICT(user_id,name) DO UPDATE SET command=excluded.command,argv_json=excluded.argv_json`, user, tool, c, b)
		if e != nil {
			return e
		}
	}
	return nil
}

package board

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func canonicalDir(root, path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("projectDirectory required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
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
func (a *App) projectDirectoryConflict(workspaceID int64, directory string) string {
	var name string
	_ = a.DB.QueryRow(`SELECT name FROM projects WHERE workspace_id=? AND directory=?`, workspaceID, directory).Scan(&name)
	return name
}
func (a *App) projects(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, e := a.DB.Query(`SELECT p.id,p.name,p.directory,w.id,w.name,
			(SELECT count(*) FROM columns c WHERE c.project_id=p.id AND c.archived=0),
			(SELECT count(*) FROM jobs j JOIN columns c ON c.lane_id=j.lane_id WHERE c.project_id=p.id AND c.archived=0 AND j.archived=0)
			FROM projects p JOIN workspaces w ON w.id=p.workspace_id JOIN workspace_members m ON m.workspace_id=w.id
			WHERE m.user_id=? ORDER BY w.name,p.name`, uid(r))
		if e != nil {
			fail(w, 500, "projects unavailable")
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, wid int64
			var name, directory, workspace string
			var columns, jobs int
			if rows.Scan(&id, &name, &directory, &wid, &workspace, &columns, &jobs) == nil {
				out = append(out, map[string]any{"id": id, "name": name, "directory": directory, "workspaceId": wid, "workspaceName": workspace, "columnCount": columns, "jobCount": jobs})
			}
		}
		jsonOut(w, 200, out)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	var x struct {
		Name, Directory   string
		WorkspaceID       int64 `json:"workspaceId"`
		LegacyWorkspaceID int64 `json:"workspace_id"`
	}
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	if x.WorkspaceID == 0 {
		x.WorkspaceID = x.LegacyWorkspaceID
	}
	if x.WorkspaceID == 0 {
		_ = a.DB.QueryRow(`SELECT workspace_id FROM workspace_members WHERE user_id=? AND role='owner' ORDER BY workspace_id LIMIT 1`, uid(r)).Scan(&x.WorkspaceID)
	}
	if x.WorkspaceID == 0 || strings.TrimSpace(x.Name) == "" {
		fail(w, 400, "workspace_id, name and directory required")
		return
	}
	role, ok := a.role(uid(r), x.WorkspaceID)
	if !ok {
		fail(w, 404, "not found")
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	directory := x.Directory
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(a.workspaceRoot(), directory)
	}
	d, _, e := canonicalDir(a.workspaceRoot(), directory)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if existing := a.projectDirectoryConflict(x.WorkspaceID, d); existing != "" {
		fail(w, http.StatusConflict, "directory is already used by "+existing)
		return
	}
	res, e := a.DB.Exec(`INSERT INTO projects(user_id,workspace_id,name,directory) VALUES(?,?,?,?)`, uid(r), x.WorkspaceID, strings.TrimSpace(x.Name), d)
	if e != nil {
		fail(w, 409, "project unavailable")
		return
	}
	id, e := res.LastInsertId()
	if e != nil {
		fail(w, 500, "project unavailable")
		return
	}
	jsonOut(w, 201, map[string]any{"id": id, "directory": d})
}
func (a *App) projectPath(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(strings.TrimPrefix(r.URL.Path, "/api/projects/"))
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var name, directory, role string
	var wid int64
	e = a.DB.QueryRow(`SELECT p.name,p.directory,p.workspace_id,m.role FROM projects p JOIN workspace_members m ON m.workspace_id=p.workspace_id WHERE p.id=? AND m.user_id=?`, id, uid(r)).Scan(&name, &directory, &wid, &role)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	if r.Method == "GET" {
		rows, queryErr := a.DB.Query(`SELECT j.id,j.task,j.state,j.attempt_count,j.created_at,j.updated_at,u.email,b.name,c.name
			FROM jobs j JOIN users u ON u.id=j.user_id JOIN columns c ON c.lane_id=j.lane_id JOIN boards b ON b.id=c.board_id
			WHERE c.project_id=? AND c.archived=0 AND j.archived=0 ORDER BY j.updated_at DESC,j.id DESC`, id)
		if queryErr != nil {
			fail(w, 500, "project unavailable")
			return
		}
		defer rows.Close()
		jobs := []map[string]any{}
		for rows.Next() {
			var jobID int64
			var attempts int
			var task, state, created, updated, creator, board, column string
			if rows.Scan(&jobID, &task, &state, &attempts, &created, &updated, &creator, &board, &column) == nil {
				jobs = append(jobs, map[string]any{"id": jobID, "task": task, "state": state, "attempt_count": attempts, "created_at": created, "updated_at": updated, "creatorName": creator, "boardName": board, "columnName": column})
			}
		}
		var workspace string
		_ = a.DB.QueryRow(`SELECT name FROM workspaces WHERE id=?`, wid).Scan(&workspace)
		jsonOut(w, 200, map[string]any{"id": id, "name": name, "directory": directory, "workspaceId": wid, "workspaceName": workspace, "role": role, "jobs": jobs})
		return
	}
	if r.Method != "PATCH" {
		fail(w, 405, "method not allowed")
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	var x struct{ Name, Directory string }
	if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
		fail(w, 400, "name and directory required")
		return
	}
	d, _, e := canonicalDir(a.workspaceRoot(), x.Directory)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if _, e = a.DB.Exec(`UPDATE projects SET name=?,directory=? WHERE id=? AND workspace_id=?`, strings.TrimSpace(x.Name), d, id, wid); e != nil {
		fail(w, 409, "project unavailable")
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "name": strings.TrimSpace(x.Name), "directory": d})
}

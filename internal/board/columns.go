package board

import (
	"crypto/rand"
	"database/sql"
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
		rows, _ := a.DB.Query(`SELECT c.id,c.name,c.position,c.paused,c.worktree_enabled,c.worktree_name,c.worktree_path,c.project_id,p.name FROM columns c LEFT JOIN projects p ON p.id=c.project_id WHERE c.board_id=? AND c.archived=0 ORDER BY c.position`, board)
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var n string
			var p int
			var paused, en bool
			var wn, wp, pn sql.NullString
			var pid sql.NullInt64
			rows.Scan(&id, &n, &p, &paused, &en, &wn, &wp, &pid, &pn)
			out = append(out, map[string]any{"id": id, "name": n, "position": p, "paused": paused, "projectId": pid.Int64, "projectName": pn.String, "worktreeEnabled": en, "worktreeName": wn.String, "effectiveDirectory": wp.String, "jobs": []Job{}})
		}
		rows.Close()
		for _, c := range out {
			jr, _ := a.DB.Query(`SELECT j.id,j.lane_id,j.task,j.done_definition,j.warning,j.state,j.position,j.attempt_count,j.created_at,j.updated_at,u.email FROM jobs j JOIN users u ON u.id=j.user_id JOIN columns c ON c.lane_id=j.lane_id WHERE c.id=? AND c.board_id=? AND j.archived=0 ORDER BY j.position`, c["id"], board)
			jobs := []Job{}
			for jr.Next() {
				var j Job
				jr.Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.Position, &j.Attempts, &j.Created, &j.Updated, &j.Creator)
				jobs = append(jobs, j)
			}
			jr.Close()
			c["jobs"] = jobs
		}
		jsonOut(w, 200, out)
		return
	}
	if r.Method == "PATCH" {
		var x struct {
			ColumnIDs []int64 `json:"columnIds"`
		}
		if decode(r, &x) != nil {
			fail(w, 400, "invalid request")
			return
		}
		var count int
		a.DB.QueryRow(`SELECT count(*) FROM columns WHERE board_id=? AND archived=0`, board).Scan(&count)
		seen := make(map[int64]bool, len(x.ColumnIDs))
		if len(x.ColumnIDs) != count {
			fail(w, 400, "columnIds must contain every active board column")
			return
		}
		for _, id := range x.ColumnIDs {
			var ok int
			if seen[id] || a.DB.QueryRow(`SELECT 1 FROM columns WHERE id=? AND board_id=? AND archived=0`, id, board).Scan(&ok) != nil {
				fail(w, 400, "columnIds must contain every active board column")
				return
			}
			seen[id] = true
		}
		tx, err := a.DB.Begin()
		if err != nil {
			fail(w, 500, "could not reorder columns")
			return
		}
		if _, err = tx.Exec(`UPDATE columns SET position=-(position+1) WHERE board_id=?`, board); err == nil {
			for position, id := range x.ColumnIDs {
				if _, err = tx.Exec(`UPDATE columns SET position=? WHERE id=? AND board_id=?`, position, id, board); err != nil {
					break
				}
			}
			if err == nil {
				_, err = tx.Exec(`UPDATE columns SET position=?+id WHERE board_id=? AND archived=1`, count, board)
			}
		}
		if err != nil {
			tx.Rollback()
			fail(w, 500, "could not reorder columns")
			return
		}
		if err = tx.Commit(); err != nil {
			fail(w, 500, "could not reorder columns")
			return
		}
		jsonOut(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	var x struct {
		Name            string `json:"name"`
		ProjectID       int64  `json:"projectId"`
		WorktreeEnabled bool   `json:"worktreeEnabled"`
		WorktreeName    string `json:"worktreeName"`
	}
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	var projectDir string
	if x.ProjectID == 0 || a.DB.QueryRow(`SELECT p.directory FROM projects p JOIN boards b ON b.workspace_id=p.workspace_id WHERE p.id=? AND b.id=?`, x.ProjectID, board).Scan(&projectDir) != nil {
		fail(w, 400, "projectId from this workspace required")
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
		project := projectDir
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
	res, e := tx.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position,worktree_enabled,worktree_name,worktree_path) VALUES(?,?,?,?,?,?,?,?,?)`, uid(r), board, laneID, x.ProjectID, x.Name, p, x.WorktreeEnabled, wn, wp)
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
	jsonOut(w, 201, map[string]any{"id": id, "name": x.Name, "projectId": x.ProjectID, "worktreeEnabled": x.WorktreeEnabled, "worktreeName": wn})
}
func (a *App) columnPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/columns/")
	id, e := pathID(rest)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	var wt bool
	var laneID, wid int64
	var path sql.NullString
	if a.DB.QueryRow(`SELECT c.lane_id,c.worktree_enabled,c.worktree_path,b.workspace_id FROM columns c JOIN boards b ON b.id=c.board_id JOIN workspace_members m ON m.workspace_id=b.workspace_id WHERE c.id=? AND m.user_id=?`, id, uid(r)).Scan(&laneID, &wt, &path, &wid) != nil {
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
			Name      *string `json:"name"`
			Paused    *bool   `json:"paused"`
			ProjectID *int64  `json:"projectId"`
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
		if x.ProjectID != nil {
			var ok int
			if a.DB.QueryRow(`SELECT 1 FROM projects WHERE id=? AND workspace_id=?`, *x.ProjectID, wid).Scan(&ok) != nil {
				fail(w, 400, "projectId from this workspace required")
				return
			}
			a.DB.Exec(`UPDATE columns SET project_id=? WHERE id=?`, *x.ProjectID, id)
		}
		jsonOut(w, 200, map[string]bool{"ok": true})
	case "DELETE":
		a.DB.Exec(`UPDATE columns SET archived=1 WHERE id=?`, id)
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}

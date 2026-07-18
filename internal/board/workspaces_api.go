package board

import (
	"net/http"
	"strconv"
	"strings"
)

func (a *App) workspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var x struct{ Name string }
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
			fail(w, 400, "name required")
			return
		}
		tx, e := a.DB.Begin()
		if e != nil {
			fail(w, 500, "workspace unavailable")
			return
		}
		res, e := tx.Exec(`INSERT INTO workspaces(user_id,name,root) VALUES(?,?,'')`, uid(r), strings.TrimSpace(x.Name))
		if e != nil {
			tx.Rollback()
			fail(w, 409, "workspace unavailable")
			return
		}
		id, e := res.LastInsertId()
		if e == nil {
			_, e = tx.Exec(`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'owner')`, id, uid(r))
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			tx.Rollback()
			fail(w, 500, "workspace unavailable")
			return
		}
		jsonOut(w, 201, map[string]any{"id": id, "name": strings.TrimSpace(x.Name), "role": "owner", "memberCount": 1, "projectCount": 0})
		return
	}
	rows, e := a.DB.Query(`SELECT w.id,w.name,m.role,(SELECT count(*) FROM workspace_members WHERE workspace_id=w.id),(SELECT count(*) FROM projects WHERE workspace_id=w.id) FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=? ORDER BY w.name`, uid(r))
	if e != nil {
		fail(w, 500, "could not list workspaces")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var n, role string
		var mc, pc int
		rows.Scan(&id, &n, &role, &mc, &pc)
		out = append(out, map[string]any{"id": id, "name": n, "role": role, "memberCount": mc, "projectCount": pc})
	}
	jsonOut(w, 200, out)
}
func (a *App) workspacePath(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/workspaces/"), "/")
	parts := strings.Split(rest, "/")
	id, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		fail(w, 404, "not found")
		return
	}
	role, ok := a.role(uid(r), id)
	if !ok {
		fail(w, 404, "not found")
		return
	}
	if len(parts) > 1 {
		switch parts[1] {
		case "projects":
			a.workspaceProjects(w, r, id)
		case "members":
			var mid int64
			if len(parts) > 2 {
				mid, _ = strconv.ParseInt(parts[2], 10, 64)
			}
			a.workspaceMembers(w, r, id, mid)
		case "invitations":
			a.invite(w, r, id)
		case "boards":
			a.workspaceBoards(w, r, id)
		default:
			fail(w, 404, "not found")
		}
		return
	}
	if r.Method == "GET" {
		var n string
		var mc, pc int
		a.DB.QueryRow(`SELECT name,(SELECT count(*) FROM workspace_members WHERE workspace_id=?),(SELECT count(*) FROM projects WHERE workspace_id=?) FROM workspaces WHERE id=?`, id, id, id).Scan(&n, &mc, &pc)
		jsonOut(w, 200, map[string]any{"id": id, "name": n, "role": role, "memberCount": mc, "projectCount": pc})
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	if r.Method == "PATCH" {
		var x struct{ Name string }
		if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" {
			fail(w, 400, "name required")
			return
		}
		a.DB.Exec(`UPDATE workspaces SET name=? WHERE id=?`, strings.TrimSpace(x.Name), id)
		jsonOut(w, 200, map[string]any{"id": id, "name": strings.TrimSpace(x.Name)})
		return
	}
	fail(w, 405, "method not allowed")
}
func (a *App) workspaceBoards(w http.ResponseWriter, r *http.Request, wid int64) {
	if r.Method != "GET" {
		fail(w, 405, "method not allowed")
		return
	}
	rows, _ := a.DB.Query(`SELECT b.id,b.name,count(c.id) FROM boards b LEFT JOIN columns c ON c.board_id=b.id WHERE b.workspace_id=? GROUP BY b.id ORDER BY b.name`, wid)
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var n string
		var c int
		rows.Scan(&id, &n, &c)
		out = append(out, map[string]any{"id": id, "name": n, "columnCount": c})
	}
	jsonOut(w, 200, out)
}

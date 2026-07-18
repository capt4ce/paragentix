package board

import (
	"net/http"
	"strings"
)

func (a *App) boards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, _ := a.DB.Query(`SELECT b.id,b.name,b.workspace_id,w.name FROM boards b JOIN workspaces w ON w.id=b.workspace_id JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=? ORDER BY b.id`, uid(r))
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, wid int64
			var n, wn string
			rows.Scan(&id, &n, &wid, &wn)
			out = append(out, map[string]any{"id": id, "name": n, "workspaceId": wid, "workspaceName": wn})
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
		if _, ok := a.role(uid(r), x.WorkspaceID); !ok {
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
	var role string
	if a.DB.QueryRow(`SELECT m.role FROM boards b JOIN workspace_members m ON m.workspace_id=b.workspace_id WHERE b.id=? AND m.user_id=?`, id, uid(r)).Scan(&role) != nil {
		fail(w, 404, "not found")
		return
	}
	if strings.HasSuffix(rest, "/columns") {
		a.columns(w, r, id)
		return
	}
	switch r.Method {
	case "DELETE":
		if role != "owner" {
			fail(w, 403, "owner required")
			return
		}
		var n int
		a.DB.QueryRow(`SELECT count(*) FROM columns WHERE board_id=?`, id).Scan(&n)
		if n > 0 {
			fail(w, 409, "board has columns")
			return
		}
		a.DB.Exec(`DELETE FROM boards WHERE id=?`, id)
		w.WriteHeader(204)
	default:
		fail(w, 405, "method not allowed")
	}
}

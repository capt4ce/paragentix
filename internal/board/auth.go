package board

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

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
	wr, _ := tx.Exec("INSERT INTO workspaces(user_id,name,root) VALUES(?,'Default',?)", id, a.Workspace)
	wid, _ := wr.LastInsertId()
	tx.Exec("INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'owner')", wid, id)
	tx.Exec("INSERT INTO projects(user_id,workspace_id,name,directory) VALUES(?,?,'Default Project',?)", id, wid, a.Workspace)
	tx.Exec("INSERT INTO boards(user_id,workspace_id,name) VALUES(?,?,'Default Board')", id, wid)
	tx.Exec("INSERT INTO lanes(user_id,name,position) VALUES(?,'Lane 1',0)", id)
	tx.Exec(`INSERT OR IGNORE INTO notifications(user_id,invitation_id,kind,title)
		SELECT ?,i.id,'invitation','Invited to workspace: '||w.name FROM workspace_invitations i JOIN workspaces w ON w.id=i.workspace_id
		WHERE i.email=? AND i.accepted_at IS NULL AND i.expires_at>datetime('now')`, id, x.Email)
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

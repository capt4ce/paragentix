package board

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Mailer interface {
	Send(to, subject, body string) error
}
type SMTPMailer struct{ Addr, User, Password, From string }

type ResendMailer struct {
	APIKey, From, URL string
	Client            *http.Client
}

func (m ResendMailer) Send(to, subject, body string) error {
	payload, e := json.Marshal(map[string]any{"from": m.From, "to": []string{to}, "subject": subject, "text": body})
	if e != nil {
		return e
	}
	url := m.URL
	if url == "" {
		url = "https://api.resend.com/emails"
	}
	req, e := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("email delivery failed: %s", resp.Status)
	}
	return nil
}

func (m SMTPMailer) Send(to, subject, body string) error {
	if m.Addr == "" || m.From == "" {
		return fmt.Errorf("email delivery unavailable")
	}
	var auth smtp.Auth
	if m.User != "" {
		host := strings.Split(m.Addr, ":")[0]
		auth = smtp.PlainAuth("", m.User, m.Password, host)
	}
	return smtp.SendMail(m.Addr, auth, m.From, []string{to}, []byte("To: "+to+"\r\nSubject: "+subject+"\r\n\r\n"+body))
}
func (a *App) mailer() Mailer {
	if a.Mailer != nil {
		return a.Mailer
	}
	if key, from := os.Getenv("RESEND_API_KEY"), os.Getenv("RESEND_FROM_EMAIL"); key != "" && from != "" {
		return ResendMailer{APIKey: key, From: from}
	}
	return SMTPMailer{os.Getenv("SMTP_ADDR"), os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASSWORD"), os.Getenv("SMTP_FROM")}
}
func (a *App) role(user, wid int64) (string, bool) {
	var r string
	e := a.DB.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, wid, user).Scan(&r)
	return r, e == nil
}
func (a *App) workspaceProjects(w http.ResponseWriter, r *http.Request, wid int64) {
	role, ok := a.role(uid(r), wid)
	if !ok {
		fail(w, 404, "not found")
		return
	}
	if r.Method == "GET" {
		rows, _ := a.DB.Query(`SELECT id,name,directory FROM projects WHERE workspace_id=? ORDER BY name`, wid)
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var n, d string
			rows.Scan(&id, &n, &d)
			out = append(out, map[string]any{"id": id, "name": n, "directory": d})
		}
		jsonOut(w, 200, out)
		return
	}
	if r.Method != "POST" || role != "owner" {
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
	if existing := a.projectDirectoryConflict(wid, d); existing != "" {
		fail(w, http.StatusConflict, "directory is already used by "+existing)
		return
	}
	res, e := a.DB.Exec(`INSERT INTO projects(user_id,workspace_id,name,directory) VALUES(?,?,?,?)`, uid(r), wid, strings.TrimSpace(x.Name), d)
	if e != nil {
		fail(w, 409, "project unavailable")
		return
	}
	id, _ := res.LastInsertId()
	jsonOut(w, 201, map[string]any{"id": id, "name": strings.TrimSpace(x.Name), "directory": d})
}
func (a *App) workspaceMembers(w http.ResponseWriter, r *http.Request, wid int64, memberID int64) {
	role, ok := a.role(uid(r), wid)
	if !ok {
		fail(w, 404, "not found")
		return
	}
	if memberID == 0 && r.Method == "GET" {
		rows, _ := a.DB.Query(`SELECT u.id,u.email,m.role,m.created_at,'member' FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=?
			UNION ALL
			SELECT NULL,i.email,NULL,i.created_at,'invited' FROM workspace_invitations i WHERE i.workspace_id=? AND i.accepted_at IS NULL AND i.expires_at>datetime('now')
			ORDER BY email`, wid, wid)
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id *int64
			var e, c, status string
			var rr *string
			rows.Scan(&id, &e, &rr, &c, &status)
			out = append(out, map[string]any{"id": id, "email": e, "role": rr, "joinedAt": c, "status": status})
		}
		jsonOut(w, 200, out)
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	if memberID == uid(r) {
		fail(w, 409, "owners cannot remove themselves")
		return
	}
	var mr string
	if a.DB.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, wid, memberID).Scan(&mr) != nil {
		fail(w, 404, "member not found")
		return
	}
	if mr == "owner" {
		var n int
		a.DB.QueryRow(`SELECT count(*) FROM workspace_members WHERE workspace_id=? AND role='owner'`, wid).Scan(&n)
		if n <= 1 {
			fail(w, 409, "workspace must retain an owner")
			return
		}
	}
	a.DB.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, wid, memberID)
	w.WriteHeader(204)
}
func (a *App) invite(w http.ResponseWriter, r *http.Request, wid int64) {
	role, ok := a.role(uid(r), wid)
	if !ok {
		fail(w, 404, "not found")
		return
	}
	if role != "owner" {
		fail(w, 403, "owner required")
		return
	}
	var x struct{ Email string }
	if decode(r, &x) != nil {
		x.Email = ""
	}
	x.Email = strings.ToLower(strings.TrimSpace(x.Email))
	address, e := mail.ParseAddress(x.Email)
	if strings.ContainsAny(x.Email, "\r\n") || e != nil || address.Address != x.Email {
		fail(w, 400, "valid email required")
		return
	}
	var me string
	a.DB.QueryRow(`SELECT email FROM users WHERE id=?`, uid(r)).Scan(&me)
	if x.Email == me {
		fail(w, 409, "cannot invite yourself")
		return
	}
	raw := token()
	var invitationID int64
	tx, e := a.DB.Begin()
	if e == nil {
		defer tx.Rollback()
		_, e = tx.Exec(`DELETE FROM workspace_invitations WHERE workspace_id=? AND email=? AND accepted_at IS NULL AND expires_at<=datetime('now')`, wid, x.Email)
	}
	if e == nil {
		var res sql.Result
		res, e = tx.Exec(`INSERT INTO workspace_invitations(workspace_id,email,token_hash,invited_by,expires_at,opened_at) VALUES(?,?,?,?,datetime('now','+7 days'),CASE WHEN EXISTS(SELECT 1 FROM users WHERE email=?) THEN CURRENT_TIMESTAMP END)`, wid, x.Email, hash(raw), uid(r), x.Email)
		if e == nil {
			invitationID, _ = res.LastInsertId()
		}
		if e == nil {
			_, e = tx.Exec(`INSERT OR IGNORE INTO notifications(user_id,invitation_id,kind,title) SELECT id,?,'invitation','Invited to workspace: '||(SELECT name FROM workspaces WHERE id=?) FROM users WHERE email=?`, invitationID, wid, x.Email)
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 409, "active invitation exists")
		return
	}
	baseURL := a.BaseURL
	if baseURL == "" {
		scheme := "http"
		forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
		if strings.EqualFold(forwardedProto, "http") || strings.EqualFold(forwardedProto, "https") {
			scheme = strings.ToLower(forwardedProto)
		} else if r.TLS != nil {
			scheme = "https"
		}
		baseURL = scheme + "://" + r.Host
	}
	inviteURL := strings.TrimRight(baseURL, "/") + "/?invite=" + url.QueryEscape(raw)
	if e = a.mailer().Send(x.Email, "Workspace invitation", inviteURL); e != nil {
		a.DB.Exec(`DELETE FROM workspace_invitations WHERE token_hash=?`, hash(raw))
		fail(w, 503, e.Error())
		return
	}
	out := map[string]any{"ok": true, "invitationId": invitationID}
	if a.Mailer != nil {
		out["token"] = raw
	}
	jsonOut(w, 201, out)
}
func (a *App) invitationPreview(w http.ResponseWriter, r *http.Request) {
	var id int64
	var email, workspace, status string
	if a.DB.QueryRow(`SELECT i.id,i.email,w.name,CASE WHEN i.accepted_at IS NULL THEN 'pending' ELSE 'accepted' END FROM workspace_invitations i JOIN workspaces w ON w.id=i.workspace_id WHERE i.token_hash=? AND (i.accepted_at IS NOT NULL OR i.expires_at>datetime('now'))`, hash(r.PathValue("token"))).Scan(&id, &email, &workspace, &status) != nil {
		fail(w, 404, "invitation unavailable")
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "email": email, "workspaceName": workspace, "status": status})
}

func (a *App) invitationByID(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil {
		fail(w, 404, "invitation unavailable")
		return
	}
	if r.Method == http.MethodPost {
		a.acceptInvitation(w, r, "id", id)
		return
	}
	var email, workspace, status string
	e = a.DB.QueryRow(`SELECT i.email,w.name,CASE WHEN i.accepted_at IS NULL THEN 'pending' ELSE 'accepted' END FROM workspace_invitations i JOIN workspaces w ON w.id=i.workspace_id JOIN users u ON u.email=i.email WHERE i.id=? AND u.id=? AND (i.accepted_at IS NOT NULL OR i.expires_at>datetime('now'))`, id, uid(r)).Scan(&email, &workspace, &status)
	if e != nil {
		fail(w, 403, "invitation unavailable for this account")
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "email": email, "workspaceName": workspace, "status": status})
}

func (a *App) activeInvitation(w http.ResponseWriter, r *http.Request) {
	tx, e := a.DB.Begin()
	if e != nil {
		fail(w, 500, "could not load invitation")
		return
	}
	defer tx.Rollback()
	var id int64
	var email, workspace string
	e = tx.QueryRow(`SELECT i.id,i.email,w.name FROM workspace_invitations i JOIN workspaces w ON w.id=i.workspace_id JOIN users u ON u.email=i.email WHERE u.id=? AND i.accepted_at IS NULL AND i.opened_at IS NULL AND i.expires_at>datetime('now') ORDER BY i.id LIMIT 1`, uid(r)).Scan(&id, &email, &workspace)
	if e != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	res, e := tx.Exec(`UPDATE workspace_invitations SET opened_at=CURRENT_TIMESTAMP WHERE id=? AND opened_at IS NULL`, id)
	if e != nil {
		fail(w, 500, "could not load invitation")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, "could not load invitation")
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "email": email, "workspaceName": workspace, "status": "pending"})
}
func (a *App) invitationAccept(w http.ResponseWriter, r *http.Request) {
	a.acceptInvitation(w, r, "token", 0)
}
func (a *App) acceptInvitation(w http.ResponseWriter, r *http.Request, lookup string, invitationID int64) {
	var id, wid int64
	var email, me string
	var e error
	if lookup == "id" {
		e = a.DB.QueryRow(`SELECT id,workspace_id,email FROM workspace_invitations WHERE id=? AND accepted_at IS NULL AND expires_at>datetime('now')`, invitationID).Scan(&id, &wid, &email)
	} else {
		e = a.DB.QueryRow(`SELECT id,workspace_id,email FROM workspace_invitations WHERE token_hash=? AND accepted_at IS NULL AND expires_at>datetime('now')`, hash(r.PathValue("token"))).Scan(&id, &wid, &email)
	}
	a.DB.QueryRow(`SELECT email FROM users WHERE id=?`, uid(r)).Scan(&me)
	if e != nil || email != me {
		fail(w, 403, "invitation unavailable for this account")
		return
	}
	tx, e := a.DB.Begin()
	if e != nil {
		fail(w, 500, "could not accept invitation")
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE workspace_invitations SET accepted_at=CURRENT_TIMESTAMP WHERE id=? AND accepted_at IS NULL AND expires_at>datetime('now')`, id)
	if e == nil {
		var n int64
		n, e = res.RowsAffected()
		if e == nil && n != 1 {
			e = fmt.Errorf("invitation already accepted")
		}
	}
	if e == nil {
		_, e = tx.Exec(`INSERT OR IGNORE INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member')`, wid, uid(r))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 409, "invitation unavailable")
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}

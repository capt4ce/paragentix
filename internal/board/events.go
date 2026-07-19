package board

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (a *App) notifications(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 50 {
		limit = 10
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if before == 0 {
		before = 1 << 62
	}
	rows, _ := a.DB.Query(`SELECT id,job_id,invitation_id,kind,title,read,created_at FROM notifications WHERE user_id=? AND id<? ORDER BY id DESC LIMIT ?`, uid(r), before, limit+1)
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var job, invitation sql.NullInt64
		var kind, title, created string
		var read bool
		rows.Scan(&id, &job, &invitation, &kind, &title, &read, &created)
		var jobID any
		if job.Valid {
			jobID = job.Int64
		}
		var invitationID any
		if invitation.Valid {
			invitationID = invitation.Int64
		}
		out = append(out, map[string]any{"id": id, "job_id": jobID, "invitation_id": invitationID, "kind": kind, "title": title, "read": read, "created_at": created})
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	var unread int
	a.DB.QueryRow("SELECT count(*) FROM notifications WHERE user_id=? AND read=0", uid(r)).Scan(&unread)
	jsonOut(w, 200, map[string]any{"notifications": out, "has_more": hasMore, "unread": unread})
}
func (a *App) notificationPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/notifications/")
	if rest == "mark-read" && r.Method == "POST" {
		a.DB.Exec("UPDATE notifications SET read=1 WHERE user_id=?", uid(r))
		jsonOut(w, 200, map[string]bool{"ok": true})
		return
	}
	id, e := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
	if e != nil || r.Method != "PATCH" {
		fail(w, 405, "method not allowed")
		return
	}
	var x struct {
		Read bool `json:"read"`
	}
	if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	res, _ := a.DB.Exec("UPDATE notifications SET read=? WHERE id=? AND user_id=?", x.Read, id, uid(r))
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "not found")
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) notify(job, run int64, kind string) {
	a.DB.Exec(`INSERT OR IGNORE INTO notifications(user_id,job_id,job_run_id,kind,title) SELECT user_id,id,?,?,CASE ? WHEN 'done' THEN 'Job completed: ' ELSE 'Job errored: ' END||task FROM jobs WHERE id=?`, run, kind, kind, job)
}
func (a *App) comment(w http.ResponseWriter, r *http.Request, id int64, state string) {
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	if state != "in_progress" && state != "blocked" && state != "done" {
		fail(w, 409, "job session is not active")
		return
	}
	var x struct{ Comment string }
	var attachments []jobAttachment
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var err error
		attachments, err = parseAttachments(w, r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		x.Comment = r.FormValue("comment")
	} else if decode(r, &x) != nil {
		fail(w, 400, "invalid request")
		return
	}
	x.Comment = strings.TrimSpace(x.Comment)
	if (x.Comment == "" && len(attachments) == 0) || len(x.Comment) > 4000 {
		fail(w, 400, "comment must be 1-4000 characters")
		return
	}
	x.Comment = appendAttachmentContext(x.Comment, attachments)
	if state == "done" {
		var run int64
		if e := a.DB.QueryRow("SELECT id FROM job_runs WHERE job_id=? ORDER BY id DESC LIMIT 1", id).Scan(&run); e != nil {
			fail(w, 409, "previous session not found")
			return
		}
		var seq int
		a.DB.QueryRow("SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_run_id=?", run).Scan(&seq)
		tx, _ := a.DB.Begin()
		if _, e := tx.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,?,?,?)", run, seq, "comment", x.Comment); e != nil {
			tx.Rollback()
			fail(w, 500, "could not record comment")
			return
		}
		if e := appendJobEventTx(tx, id, "status", statusContent("done", "todo")); e != nil {
			tx.Rollback()
			fail(w, 500, "could not record status")
			return
		}
		tx.Exec(`UPDATE jobs SET state='todo',position=(SELECT COALESCE(MAX(position)+1,0) FROM jobs WHERE lane_id=(SELECT lane_id FROM jobs WHERE id=?)),pending_comment=?,finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id, x.Comment, id)
		tx.Commit()
		jsonOut(w, 200, map[string]bool{"ok": true})
		a.signal()
		return
	}
	var run int64
	var session string
	if e := a.DB.QueryRow("SELECT id,tmux_session FROM job_runs WHERE job_id=? AND status='running' ORDER BY id DESC LIMIT 1", id).Scan(&run, &session); e != nil {
		fail(w, 409, "active session not found")
		return
	}
	if e := exec.Command("tmux", "has-session", "-t", session).Run(); e != nil {
		fail(w, 409, "active session not found")
		return
	}
	if e := exec.Command("tmux", "send-keys", "-t", session, "-l", x.Comment).Run(); e != nil {
		fail(w, 500, "could not send comment")
		return
	}
	if e := exec.Command("tmux", "send-keys", "-t", session, "Enter").Run(); e != nil {
		fail(w, 500, "could not send comment")
		return
	}
	var seq int
	a.DB.QueryRow("SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_run_id=?", run).Scan(&seq)
	a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,?,?,?)", run, seq, "comment", x.Comment)
	jsonOut(w, 200, map[string]bool{"ok": true})
	a.signal()
}
func (a *App) events(w http.ResponseWriter, id int64) {
	rows, _ := a.DB.Query("SELECT e.id,CASE WHEN e.kind='output' AND r.tmux_session LIKE 'hermes-api:%' THEN 'reply' ELSE e.kind END,e.content,e.created_at FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? ORDER BY e.id", id)
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
		rows, _ := a.DB.Query("SELECT e.id,CASE WHEN e.kind='output' AND x.tmux_session LIKE 'hermes-api:%' THEN 'reply' ELSE e.kind END,e.content FROM job_events e JOIN job_runs x ON x.id=e.job_run_id WHERE x.job_id=? AND e.id>? ORDER BY e.id", id, last)
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

package board

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

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
		jr, _ := a.DB.Query("SELECT j.id,j.lane_id,j.task,j.done_definition,j.warning,j.state,j.position,j.attempt_count,j.created_at,j.updated_at,u.email FROM jobs j JOIN users u ON u.id=j.user_id WHERE j.lane_id=? ORDER BY CASE j.state WHEN 'in_progress' THEN 0 WHEN 'blocked' THEN 1 WHEN 'todo' THEN 2 ELSE 3 END,j.position", l.ID)
		for jr.Next() {
			var j Job
			jr.Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.Position, &j.Attempts, &j.Created, &j.Updated, &j.Creator)
			l.Jobs = append(l.Jobs, j)
		}
		jr.Close()
		out = append(out, l)
	}
	jsonOut(w, 200, out)
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
	var p int
	a.DB.QueryRow("SELECT COALESCE(MAX(position)+1,0) FROM jobs WHERE lane_id=?", lane).Scan(&p)
	warning := ""
	if strings.TrimSpace(x.DoneDefinition) == "" {
		warning = "Completion criteria generation deferred: add criteria manually or run the task as-is."
	}
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,done_definition,warning,position) VALUES(?,?,?,?,?,?)", uid(r), lane, strings.TrimSpace(x.Task), strings.TrimSpace(x.DoneDefinition), warning, p)
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
		if r.Method == "DELETE" {
			exec.Command("tmux", "kill-session", "-t", fmt.Sprintf("agent-job-%d", id)).Run()
			tx, e := a.DB.Begin()
			if e == nil {
				_, e = tx.Exec("DELETE FROM job_events WHERE job_run_id IN (SELECT id FROM job_runs WHERE job_id=?)", id)
			}
			if e == nil {
				_, e = tx.Exec("DELETE FROM job_runs WHERE job_id=?", id)
			}
			if e == nil {
				_, e = tx.Exec("DELETE FROM jobs WHERE id=? AND user_id=?", id, uid(r))
			}
			if e == nil {
				e = tx.Commit()
			}
			if e != nil {
				if tx != nil {
					tx.Rollback()
				}
				fail(w, 500, "could not archive job")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			a.signal()
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
		case "comment":
			a.comment(w, r, id, state)
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
	a.DB.QueryRow("SELECT j.id,j.lane_id,j.task,j.done_definition,j.warning,j.state,j.position,j.attempt_count,j.created_at,j.updated_at,u.email FROM jobs j JOIN users u ON u.id=j.user_id WHERE j.id=?", id).Scan(&j.ID, &j.LaneID, &j.Task, &j.Done, &j.Warning, &j.State, &j.Position, &j.Attempts, &j.Created, &j.Updated, &j.Creator)
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
	} else if act == "retry" {
		exec.Command("tmux", "kill-session", "-t", fmt.Sprintf("agent-job-%d", id)).Run()
		a.DB.Exec("UPDATE jobs SET state='todo',finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
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

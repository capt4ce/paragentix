package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

func (a *App) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *App) scheduler() {
	defer a.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		a.schedule()
		select {
		case <-a.stop:
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}
func (a *App) schedule() {
	rows, e := a.DB.Query(`SELECT j.id,j.task,j.done_definition,s.workspace_root,j.pending_comment FROM jobs j JOIN lanes l ON l.id=j.lane_id JOIN user_settings s ON s.user_id=j.user_id WHERE j.state='todo' AND j.archived=0 AND l.paused=0 AND NOT EXISTS(SELECT 1 FROM jobs x WHERE x.lane_id=j.lane_id AND x.state IN('in_progress','blocked') AND x.archived=0) AND j.id=(SELECT id FROM jobs q WHERE q.lane_id=j.lane_id AND q.state='todo' AND q.archived=0 ORDER BY q.position LIMIT 1)`)
	if e != nil {
		return
	}
	type q struct {
		id                        int64
		task, done, root, comment string
	}
	var qs []q
	for rows.Next() {
		var x q
		rows.Scan(&x.id, &x.task, &x.done, &x.root, &x.comment)
		qs = append(qs, x)
	}
	rows.Close()
	for _, x := range qs {
		if x.comment != "" {
			x.task += "\n\nFollow-up reply:\n" + x.comment
		}
		a.start(x.id, x.task, x.done, x.root)
	}
}
func (a *App) runHermes(ctx context.Context, userID int64, prompt string) (string, error) {
	return a.runHermesSession(ctx, userID, prompt, "")
}
func (a *App) runHermesSession(ctx context.Context, userID int64, prompt, sessionID string) (string, error) {
	var base, key, model string
	if e := a.DB.QueryRow("SELECT hermes_url,hermes_api_key,hermes_model FROM user_settings WHERE user_id=?", userID).Scan(&base, &key, &model); e != nil {
		return "", e
	}
	body, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": prompt}}})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")+"/v1/chat/completions", strings.NewReader(string(body)))
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("X-Hermes-Session-Id", sessionID)
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if e != nil {
		return "", e
	}
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("Hermes API error %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if e = json.Unmarshal(b, &out); e != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("invalid Hermes API response")
	}
	return out.Choices[0].Message.Content, nil
}

func (a *App) start(id int64, task, done, root string) {
	var effective, projectName, projectDirectory string
	if err := a.DB.QueryRow(`SELECT CASE WHEN c.worktree_enabled=1 THEN c.worktree_path ELSE p.directory END,p.name,p.directory FROM jobs j JOIN columns c ON c.lane_id=j.lane_id JOIN projects p ON p.id=c.project_id WHERE j.id=?`, id).Scan(&effective, &projectName, &projectDirectory); err != nil || effective == "" {
		a.DB.Exec("UPDATE jobs SET state='blocked',warning='Selected project or worktree is unavailable',updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
		a.appendJobEvent(id, "status", statusContent("todo", "blocked"))
		return
	}
	_, validated, err := canonicalDir(a.workspaceRoot(), effective)
	if err != nil {
		return
	}
	root = validated
	a.startHermes(id, initialHermesPrompt(projectName, projectDirectory, task, done))
}

func initialHermesPrompt(projectName, projectDirectory, task, done string) string {
	return fmt.Sprintf("Unless otherwise specified, this conversation concerns the project %s, located at %s. Use this project as the default when creating or modifying jobs. Use the direct terminal tool with %s as the workdir for shell commands; do not wrap terminal in execute_code. Delegated shell work must request terminal explicitly. If an indirect terminal attempt fails, retry with the direct terminal tool before claiming terminal is unavailable.\n\n%s\n\nDone definition:\n%s", projectName, projectDirectory, projectDirectory, task, done)
}
func (a *App) startHermes(id int64, prompt string) {
	sessionID := token()
	tx, _ := a.DB.Begin()
	res, e := tx.Exec("UPDATE jobs SET state='in_progress',pending_comment='',attempt_count=attempt_count+1,started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='todo'", id)
	if e != nil {
		tx.Rollback()
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return
	}
	var attempt int
	tx.QueryRow("SELECT attempt_count FROM jobs WHERE id=?", id).Scan(&attempt)
	rr, _ := tx.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,?,?,'running')", id, attempt, "hermes-api:"+sessionID)
	run, _ := rr.LastInsertId()
	if e = appendJobEventTx(tx, id, "status", statusContent("todo", "in_progress")); e != nil {
		tx.Rollback()
		return
	}
	tx.Commit()
	a.runHermesJob(id, run, sessionID, prompt)
}

func (a *App) retryHermes(id int64, state string) error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var run int64
	var session string
	if err = tx.QueryRow("SELECT id,tmux_session FROM job_runs WHERE job_id=? AND tmux_session LIKE 'hermes-api:%' ORDER BY id DESC LIMIT 1", id).Scan(&run, &session); err != nil {
		return err
	}
	session = strings.TrimPrefix(session, "hermes-api:")
	if session == "" {
		return sql.ErrNoRows
	}
	if _, err = tx.Exec("UPDATE jobs SET state='in_progress',warning='',finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE job_runs SET status='running',ended_at=NULL,result_summary='' WHERE id=?", run); err != nil {
		return err
	}
	if state != "in_progress" {
		if err = appendJobEventTx(tx, id, "status", statusContent(state, "in_progress")); err != nil {
			return err
		}
	}
	if err = appendJobEventTx(tx, id, "retry", "Job retried"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.runHermesJob(id, run, session, "retry")
	return nil
}

func (a *App) runHermesJob(id, run int64, sessionID, prompt string) {
	go func() {
		var user int64
		a.DB.QueryRow("SELECT user_id FROM jobs WHERE id=?", id).Scan(&user)
		out, e := a.runHermesSession(context.Background(), user, prompt, sessionID)
		if e != nil {
			a.block(id, run, e.Error())
			return
		}
		a.appendJobEvent(id, "reply", out)
		a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", out, run)
		a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
		a.appendJobEvent(id, "status", statusContent("in_progress", "done"))
		a.notify(id, run, "done")
		a.signal()
	}()
}

func (a *App) monitor(job, run int64, session string) {
	seq := 0
	a.DB.QueryRow("SELECT COALESCE(MAX(sequence),0) FROM job_events WHERE job_run_id=?", run).Scan(&seq)
	last := ""
	for i := 0; i < 3600; i++ {
		time.Sleep(time.Second)
		out, e := exec.Command("tmux", "capture-pane", "-p", "-t", session, "-S", "-200").Output()
		text := string(out)
		if text != last {
			seq++
			delta := text
			if strings.HasPrefix(text, last) {
				delta = strings.TrimPrefix(text, last)
			}
			a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,?,?,?)", run, seq, "output", delta)
			last = text
		}
		if e != nil {
			a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", last, run)
			res, _ := a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='in_progress'", job)
			if changed, _ := res.RowsAffected(); changed > 0 {
				a.appendJobEvent(job, "status", statusContent("in_progress", "done"))
			}
			a.notify(job, run, "done")
			a.signal()
			return
		}
	}
	a.block(job, run, "execution timed out")
}
func (a *App) block(job, run int64, msg string) {
	var old string
	a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&old)
	a.DB.Exec("UPDATE job_runs SET status='blocked',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", msg, run)
	a.appendJobEvent(job, "error", msg)
	a.DB.Exec("UPDATE jobs SET state='blocked',warning=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", msg, job)
	if old != "blocked" {
		a.appendJobEvent(job, "status", statusContent(old, "blocked"))
	}
	a.notify(job, run, "error")
}
func (a *App) reconcile() {
	rows, _ := a.DB.Query(`SELECT r.id,r.job_id,r.tmux_session FROM job_runs r JOIN jobs j ON j.id=r.job_id WHERE r.status='running' OR (r.status='blocked' AND r.tmux_session LIKE 'hermes-api:%' AND j.state='blocked' AND j.warning='Execution session missing after server restart')`)
	defer rows.Close()
	type pending struct {
		run, job int64
		session  string
	}
	var hermes []pending
	for rows.Next() {
		var run, job int64
		var session string
		rows.Scan(&run, &job, &session)
		if strings.HasPrefix(session, "hermes-api:") {
			hermes = append(hermes, pending{run, job, strings.TrimPrefix(session, "hermes-api:")})
			continue
		}
		if exec.Command("tmux", "has-session", "-t", session).Run() != nil {
			a.block(job, run, "Execution session missing after server restart")
		}
	}
	rows.Close()
	for _, x := range hermes {
		if a.reconcileHermes(x.job, x.run, x.session) {
			a.wg.Add(1)
			go a.watchHermes(x.job, x.run, x.session)
		}
	}
}

type hermesSession struct {
	Session struct {
		EndedAt   any    `json:"ended_at"`
		EndReason string `json:"end_reason"`
	} `json:"session"`
}
type hermesMessages struct {
	Data []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"data"`
}

func (a *App) hermesGet(user int64, path string, out any) error {
	var base, key string
	if err := a.DB.QueryRow("SELECT hermes_url,hermes_api_key FROM user_settings WHERE user_id=?", user).Scan(&base, &key); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("Hermes API error %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

func (a *App) reconcileHermes(job, run int64, sessionID string) bool {
	var user int64
	if err := a.DB.QueryRow("SELECT user_id FROM jobs WHERE id=?", job).Scan(&user); err != nil {
		return false
	}
	var session hermesSession
	var messages hermesMessages
	if a.hermesGet(user, "/api/sessions/"+sessionID, &session) != nil || a.hermesGet(user, "/api/sessions/"+sessionID+"/messages", &messages) != nil {
		return false
	}
	output := ""
	for _, message := range messages.Data {
		if message.Role == "user" {
			output = ""
		}
		if message.Role == "assistant" {
			switch content := message.Content.(type) {
			case string:
				output = content
			default:
				if b, err := json.Marshal(content); err == nil {
					output = string(b)
				}
			}
		}
	}
	if output != "" {
		var count int
		a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='reply' AND content=?", run, output).Scan(&count)
		if count == 0 {
			a.appendJobEvent(job, "reply", output)
		}
		a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", output, run)
		var old string
		a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&old)
		res, _ := a.DB.Exec("UPDATE jobs SET state='done',warning='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN ('blocked','in_progress')", job)
		if changed, _ := res.RowsAffected(); changed > 0 {
			a.appendJobEvent(job, "status", statusContent(old, "done"))
		}
		a.notify(job, run, "done")
		a.signal()
		return false
	}
	if session.Session.EndedAt != nil || session.Session.EndReason != "" {
		msg := session.Session.EndReason
		if msg == "" {
			msg = "Hermes session ended without a response"
		}
		a.block(job, run, msg)
		return false
	}
	res, _ := a.DB.Exec("UPDATE jobs SET state='in_progress',warning='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='blocked'", job)
	if changed, _ := res.RowsAffected(); changed > 0 {
		a.appendJobEvent(job, "status", statusContent("blocked", "in_progress"))
	}
	a.DB.Exec("UPDATE job_runs SET status='running',ended_at=NULL,result_summary='' WHERE id=?", run)
	return true
}

func (a *App) watchHermes(job, run int64, sessionID string) {
	defer a.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-tick.C:
			if !a.reconcileHermes(job, run, sessionID) {
				return
			}
		}
	}
}

package board

import (
	"context"
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
	rows, e := a.DB.Query(`SELECT j.id,j.task,j.done_definition,s.workspace_root,j.pending_comment FROM jobs j JOIN lanes l ON l.id=j.lane_id JOIN user_settings s ON s.user_id=j.user_id WHERE j.state='todo' AND l.paused=0 AND NOT EXISTS(SELECT 1 FROM jobs x WHERE x.lane_id=j.lane_id AND x.state IN('in_progress','blocked')) AND j.id=(SELECT id FROM jobs q WHERE q.lane_id=j.lane_id AND q.state='todo' ORDER BY q.position LIMIT 1)`)
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
	rr, _ := tx.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,?,?,'running')", id, attempt, "hermes-api")
	run, _ := rr.LastInsertId()
	tx.Commit()
	go func() {
		var user int64
		a.DB.QueryRow("SELECT user_id FROM jobs WHERE id=?", id).Scan(&user)
		out, e := a.runHermes(context.Background(), user, prompt)
		if e != nil {
			a.block(id, run, e.Error())
			return
		}
		a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'output',?)", run, out)
		a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", out, run)
		a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?", id)
		a.notify(id, run, "done")
		a.signal()
	}()
}

func (a *App) monitor(job, run int64, session string) {
	seq := 0
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
			a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='in_progress'", job)
			a.notify(job, run, "done")
			a.signal()
			return
		}
	}
	a.block(job, run, "execution timed out")
}
func (a *App) block(job, run int64, msg string) {
	a.DB.Exec("UPDATE job_runs SET status='blocked',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", msg, run)
	a.DB.Exec("INSERT OR IGNORE INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'error',?)", run, msg)
	a.DB.Exec("UPDATE jobs SET state='blocked',warning=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", msg, job)
	a.notify(job, run, "error")
}
func (a *App) reconcile() {
	rows, _ := a.DB.Query("SELECT id,job_id,tmux_session FROM job_runs WHERE status='running'")
	defer rows.Close()
	for rows.Next() {
		var run, job int64
		var session string
		rows.Scan(&run, &job, &session)
		if exec.Command("tmux", "has-session", "-t", session).Run() != nil {
			a.block(job, run, "Execution session missing after server restart")
		}
	}
}

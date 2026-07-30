package board

import (
	"context"
	"crypto/sha256"
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
			if a.resumeHermesFeedback(x.id, x.comment) == nil {
				continue
			}
		}
		a.start(x.id, x.task, x.done, x.root)
	}
}
func (a *App) runHermes(ctx context.Context, workspaceID int64, prompt string) (string, error) {
	return a.runHermesSession(ctx, workspaceID, prompt, "")
}
func (a *App) runHermesSession(ctx context.Context, workspaceID int64, prompt, sessionID string) (string, error) {
	var base, key, model string
	if e := a.DB.QueryRow("SELECT hermes_url,hermes_api_key,hermes_model FROM workspaces WHERE id=?", workspaceID).Scan(&base, &key, &model); e != nil {
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
	attachments := []jobAttachment{}
	rows, err := a.DB.Query("SELECT name,content FROM job_attachments WHERE job_id=? ORDER BY id", id)
	if err != nil {
		return
	}
	for rows.Next() {
		var attachment jobAttachment
		if err = rows.Scan(&attachment.Name, &attachment.Content); err != nil {
			rows.Close()
			return
		}
		attachments = append(attachments, attachment)
	}
	rows.Close()
	a.startHermes(id, initialHermesPrompt(projectName, projectDirectory, task, done, attachments))
}

func initialHermesPrompt(projectName, projectDirectory, task, done string, attachmentSets ...[]jobAttachment) string {
	doneDefinition := ""
	if len(done) != 0 {
		doneDefinition = fmt.Sprintf("\n\nDone definition:\n%s", done)
	}
	prompt := fmt.Sprintf("Unless otherwise specified, this conversation concerns the project %s, located at %s. Use this project as the default when creating or modifying jobs. Use the direct terminal tool with %s as the workdir for shell commands; do not wrap terminal in execute_code. Delegated shell work must request terminal explicitly. If an indirect terminal attempt fails, retry with the direct terminal tool before claiming terminal is unavailable.\n\nDo not implement the requested work yet. Analyze it, inspect the project as needed, and return a concrete proposal for explicit review and approval.\n\n%s%s", projectName, projectDirectory, projectDirectory, task, doneDefinition)
	if len(attachmentSets) > 0 && len(attachmentSets[0]) > 0 {
		prompt = appendAttachmentContext(prompt, attachmentSets[0])
	}
	return prompt
}
func (a *App) startHermes(id int64, prompt string) {
	sessionID := token()
	tx, _ := a.DB.Begin()
	res, e := tx.Exec("UPDATE jobs SET state='in_progress',phase='review',pending_comment='',attempt_count=attempt_count+1,started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='todo'", id)
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

func (a *App) resumeHermesFeedback(id int64, feedback string) error {
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
	res, err := tx.Exec(`UPDATE jobs SET state='in_progress',pending_comment='',warning='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='todo' AND pending_comment<>''`, id)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.Exec("UPDATE job_runs SET status='running',ended_at=NULL,result_summary='' WHERE id=?", run); err != nil {
		return err
	}
	if err = appendJobEventTx(tx, id, "status", statusContent("todo", "in_progress")); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.runHermesJob(id, run, session, "Review the following feedback, revise your proposal, and return it for review. Do not implement yet.\n\n"+feedback)
	return nil
}

func (a *App) approveHermes(id int64) (bool, error) {
	tx, err := a.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, phase, session string
	var run int64
	if err = tx.QueryRow(`SELECT j.state,j.phase,r.id,r.tmux_session FROM jobs j JOIN job_runs r ON r.job_id=j.id
		WHERE j.id=? AND r.id=(SELECT id FROM job_runs WHERE job_id=j.id ORDER BY id DESC LIMIT 1)`, id).Scan(&state, &phase, &run, &session); err != nil {
		return false, err
	}
	if state == "in_progress" && phase == "implementation" {
		return false, nil
	}
	if state != "in_review" || phase != "review" || !strings.HasPrefix(session, "hermes-api:") {
		return false, fmt.Errorf("job is not awaiting review")
	}
	res, err := tx.Exec(`UPDATE jobs SET state='in_progress',phase='implementation',warning='',finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='in_review' AND phase='review'`, id)
	if err != nil {
		return false, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return false, fmt.Errorf("review state changed")
	}
	if _, err = tx.Exec(`UPDATE job_runs SET status='running',ended_at=NULL,result_summary='' WHERE id=?`, run); err != nil {
		return false, err
	}
	if err = appendJobEventTx(tx, id, "status", statusContent("in_review", "in_progress")); err != nil {
		return false, err
	}
	if err = appendJobEventTx(tx, id, "approval", "Implementation approved"); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	a.runHermesJob(id, run, strings.TrimPrefix(session, "hermes-api:"), "The proposal is explicitly approved. Implement it now, verify the work, and report the completed result.")
	return true, nil
}

func (a *App) runHermesJob(id, run int64, sessionID, prompt string) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		var workspace int64
		if err := a.DB.QueryRow("SELECT b.workspace_id FROM jobs j JOIN columns c ON c.lane_id=j.lane_id JOIN boards b ON b.id=c.board_id WHERE j.id=?", id).Scan(&workspace); err != nil {
			a.block(id, run, err.Error())
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type completion struct {
			output string
			err    error
		}
		completed := make(chan completion, 1)
		go func() {
			output, err := a.runHermesSession(ctx, workspace, prompt, sessionID)
			completed <- completion{output: output, err: err}
		}()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		a.syncHermesIntermediaries(id, run, sessionID, "")
		for {
			select {
			case <-a.stop:
				return
			case result := <-completed:
				if !a.hermesRunActive(id, run) {
					return
				}
				if result.err != nil {
					a.block(id, run, result.err.Error())
					return
				}
				a.syncHermesIntermediaries(id, run, sessionID, result.output)
				_ = a.syncHermesTitle(id, sessionID)
				a.finishHermesRun(id, run, result.output)
				a.signal()
				return
			case <-tick.C:
				if !a.hermesRunActive(id, run) {
					return
				}
				_ = a.syncHermesTitle(id, sessionID)
				a.syncHermesIntermediaries(id, run, sessionID, "")
			}
		}
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
			a.DB.Exec(`INSERT INTO job_events(job_run_id,sequence,kind,content,conversation_id)
				SELECT ?,?,?,?,id FROM job_conversations WHERE job_id=? AND parent_conversation_id IS NULL`, run, seq, "output", delta, job)
			last = text
		}
		if e != nil {
			a.DB.Exec("UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=?", last, run)
			res, _ := a.DB.Exec("UPDATE jobs SET state='in_review',finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='in_progress' AND phase='review'", job)
			if changed, _ := res.RowsAffected(); changed > 0 {
				a.appendJobEvent(job, "status", statusContent("in_progress", "in_review"))
			}
			a.notify(job, run, "review")
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
		EndedAt   any     `json:"ended_at"`
		EndReason string  `json:"end_reason"`
		Title     *string `json:"title"`
	} `json:"session"`
}
type hermesMessages struct {
	Data []hermesMessage `json:"data"`
}
type hermesMessage struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type normalizedHermesMessage struct {
	SourceKey string
	Content   string
}

func normalizeHermesMessages(messages []hermesMessage) ([]normalizedHermesMessage, string) {
	assistant := normalizeHermesAssistantMessages(messages)
	if len(assistant) == 0 {
		return nil, ""
	}
	final := assistant[len(assistant)-1].Content
	return assistant[:len(assistant)-1], final
}

func normalizeHermesAssistantMessages(messages []hermesMessage) []normalizedHermesMessage {
	assistant := make([]normalizedHermesMessage, 0)
	for index, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		content, ok := message.Content.(string)
		if !ok {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		sourceKey := message.ID
		if sourceKey == "" {
			sourceKey = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", message.Role, index, content))))
		}
		assistant = append(assistant, normalizedHermesMessage{SourceKey: sourceKey, Content: content})
	}
	return assistant
}

func (a *App) hermesRunActive(job, run int64) bool {
	var active int
	_ = a.DB.QueryRow(`SELECT count(*) FROM job_runs r JOIN jobs j ON j.id=r.job_id
		WHERE r.id=? AND r.job_id=? AND r.status='running' AND j.state='in_progress'
		AND r.id=(SELECT id FROM job_runs WHERE job_id=? ORDER BY id DESC LIMIT 1)`, run, job, job).Scan(&active)
	return active == 1
}

func (a *App) syncHermesIntermediaries(job, run int64, sessionID, finalOutput string) error {
	var messages hermesMessages
	if err := a.hermesGet(job, "/api/sessions/"+sessionID+"/messages", &messages); err != nil {
		return err
	}
	return a.persistHermesIntermediaries(job, run, messages.Data, finalOutput)
}

func (a *App) syncHermesTitle(job int64, sessionID string) error {
	var session hermesSession
	if err := a.hermesGet(job, "/api/sessions/"+sessionID, &session); err != nil {
		return err
	}
	if session.Session.Title == nil || strings.TrimSpace(*session.Session.Title) == "" {
		return nil
	}
	_, err := a.DB.Exec(`UPDATE jobs SET title=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, strings.TrimSpace(*session.Session.Title), job)
	return err
}

func (a *App) finishHermesRun(job, run int64, output string) error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var phase, old string
	if err = tx.QueryRow(`SELECT j.phase,j.state FROM jobs j JOIN job_runs r ON r.job_id=j.id
		WHERE j.id=? AND r.id=? AND j.state IN('in_progress','blocked') AND r.status IN('running','blocked')
		AND r.id=(SELECT id FROM job_runs WHERE job_id=j.id ORDER BY id DESC LIMIT 1)`, job, run).Scan(&phase, &old); err != nil {
		return err
	}
	next, kind := "in_review", "review"
	if phase == "implementation" {
		next, kind = "done", "done"
	}
	if err = appendJobEventTx(tx, job, "reply", output); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE job_runs SET status='done',ended_at=CURRENT_TIMESTAMP,result_summary=? WHERE id=? AND status IN('running','blocked')`, output, run); err != nil {
		return err
	}
	if next == "done" {
		_, err = tx.Exec(`UPDATE jobs SET state='done',warning='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN('in_progress','blocked') AND phase='implementation'`, job)
	} else {
		_, err = tx.Exec(`UPDATE jobs SET state='in_review',warning='',finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN('in_progress','blocked') AND phase='review'`, job)
	}
	if err != nil {
		return err
	}
	if err = appendJobEventTx(tx, job, "status", statusContent(old, next)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.notify(job, run, kind)
	return nil
}

func (a *App) persistHermesIntermediaries(job, run int64, messages []hermesMessage, finalOutput string) error {
	intermediaries := normalizeHermesAssistantMessages(messages)
	if len(intermediaries) == 0 {
		return nil
	}
	if finalOutput == "" || intermediaries[len(intermediaries)-1].Content == strings.TrimSpace(finalOutput) {
		intermediaries = intermediaries[:len(intermediaries)-1]
	}
	if len(intermediaries) == 0 {
		return nil
	}
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var conversation sql.NullInt64
	if err = tx.QueryRow("SELECT id FROM job_conversations WHERE job_id=? AND parent_conversation_id IS NULL", job).Scan(&conversation); err != nil && err != sql.ErrNoRows {
		return err
	}
	for _, message := range intermediaries {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO job_events(job_run_id,sequence,kind,content,conversation_id,source_message_key)
			SELECT ?,COALESCE(MAX(sequence),0)+1,'intermediary',?,?,? FROM job_events WHERE job_run_id=?`,
			run, message.Content, conversation, message.SourceKey, run); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) hermesGet(job int64, path string, out any) error {
	var base, key string
	if err := a.DB.QueryRow("SELECT w.hermes_url,w.hermes_api_key FROM jobs j JOIN columns c ON c.lane_id=j.lane_id JOIN boards b ON b.id=c.board_id JOIN workspaces w ON w.id=b.workspace_id WHERE j.id=?", job).Scan(&base, &key); err != nil {
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
	var session hermesSession
	var messages hermesMessages
	if a.hermesGet(job, "/api/sessions/"+sessionID, &session) != nil || a.hermesGet(job, "/api/sessions/"+sessionID+"/messages", &messages) != nil {
		return false
	}
	_, output := normalizeHermesMessages(messages.Data)
	if output != "" {
		_ = a.persistHermesIntermediaries(job, run, messages.Data, output)
		_ = a.syncHermesTitle(job, sessionID)
		_ = a.finishHermesRun(job, run, output)
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

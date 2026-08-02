package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const maxForksPerBatch = 10
const maxMergePointBytes = 1000
const maxMergeSummaryBytes = 20000
const conversationCompletionAttempts = 3

var errConversationCompletionStale = errors.New("conversation completion is no longer current")

type Conversation struct {
	ID                   int64  `json:"id"`
	JobID                int64  `json:"jobId"`
	ParentConversationID *int64 `json:"parentConversationId"`
	ForkEventID          *int64 `json:"forkEventId"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type ConversationProgress struct {
	Total        int              `json:"total"`
	Resolved     int              `json:"resolved"`
	Active       int              `json:"active"`
	Waiting      int              `json:"waiting"`
	ReadyToMerge int              `json:"readyToMerge"`
	Merged       int              `json:"merged"`
	Actionable   []map[string]any `json:"actionable,omitempty"`
}

func (a *App) conversationPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
	id, err := pathID(rest)
	if err != nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 {
		fail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	write := r.Method != http.MethodGet
	jobID, state, archived, err := a.authorizedConversation(r, id, write)
	if err != nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if write && archived {
		fail(w, http.StatusConflict, "job is archived")
		return
	}
	switch parts[1] {
	case "forks":
		if r.Method == http.MethodPost {
			a.createForks(w, r, id, jobID)
			return
		}
	case "events":
		if r.Method == http.MethodGet {
			a.conversationEvents(w, id)
			return
		}
	case "stream":
		if r.Method == http.MethodGet {
			a.conversationStream(w, r, id)
			return
		}
	case "comment":
		if r.Method == http.MethodPost {
			var isMain int
			a.DB.QueryRow(`SELECT parent_conversation_id IS NULL FROM job_conversations WHERE id=?`, id).Scan(&isMain)
			if isMain != 0 {
				a.comment(w, r, jobID, state)
				return
			}
			a.conversationComment(w, r, id, jobID)
			return
		}
	case "merge-preview":
		if r.Method == http.MethodPost {
			a.mergePreview(w, id)
			return
		}
	case "merge":
		if r.Method == http.MethodPost {
			a.mergeConversation(w, r, id, jobID)
			return
		}
	}
	fail(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) authorizedConversation(r *http.Request, conversationID int64, write bool) (int64, string, bool, error) {
	query := `SELECT c.job_id,j.state,j.archived FROM job_conversations c JOIN jobs j ON j.id=c.job_id WHERE c.id=? AND j.user_id=?`
	args := []any{conversationID, uid(r)}
	if !write {
		query = `SELECT c.job_id,j.state,j.archived FROM job_conversations c JOIN jobs j ON j.id=c.job_id WHERE c.id=? AND (j.user_id=? OR EXISTS(
			SELECT 1 FROM columns x JOIN boards b ON b.id=x.board_id JOIN workspace_members m ON m.workspace_id=b.workspace_id
			WHERE x.lane_id=j.lane_id AND m.user_id=?))`
		args = append(args, uid(r))
	}
	var jobID int64
	var state string
	var archived bool
	err := a.DB.QueryRow(query, args...).Scan(&jobID, &state, &archived)
	return jobID, state, archived, err
}

func (a *App) jobConversations(w http.ResponseWriter, jobID int64) {
	conversations, err := a.loadConversations(jobID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not load conversations")
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"conversations": conversations,
		"progress":      a.conversationProgress(jobID, conversations),
	})
}

func (a *App) loadConversations(jobID int64) ([]Conversation, error) {
	rows, err := a.DB.Query(`SELECT id,job_id,parent_conversation_id,fork_event_id,title,status,created_at,updated_at
		FROM job_conversations WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conversations := []Conversation{}
	for rows.Next() {
		var c Conversation
		var parent, fork sql.NullInt64
		if err = rows.Scan(&c.ID, &c.JobID, &parent, &fork, &c.Title, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if parent.Valid {
			c.ParentConversationID = &parent.Int64
		}
		if fork.Valid {
			c.ForkEventID = &fork.Int64
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (a *App) conversationProgressForJob(jobID int64) *ConversationProgress {
	conversations, err := a.loadConversations(jobID)
	if err != nil {
		return nil
	}
	progress := a.conversationProgress(jobID, conversations)
	if progress.Total == 0 {
		return nil
	}
	return &progress
}

func (a *App) conversationProgress(jobID int64, conversations []Conversation) ConversationProgress {
	progress := ConversationProgress{Actionable: []map[string]any{}}
	byID := map[int64]Conversation{}
	for _, c := range conversations {
		byID[c.ID] = c
	}
	for _, c := range conversations {
		if c.ParentConversationID == nil {
			continue
		}
		progress.Total++
		switch c.Status {
		case "merged":
			var eventWatermark, mergeWatermark int64
			a.DB.QueryRow(`SELECT COALESCE(MAX(id),0) FROM job_events WHERE conversation_id=?`, c.ID).Scan(&eventWatermark)
			a.DB.QueryRow(`SELECT COALESCE(MAX(source_event_watermark),0) FROM conversation_merges WHERE source_conversation_id=?`, c.ID).Scan(&mergeWatermark)
			if eventWatermark <= mergeWatermark {
				progress.Resolved++
				progress.Merged++
				continue
			}
			progress.Active++
		case "waiting":
			progress.Waiting++
		case "ready_to_merge":
			progress.ReadyToMerge++
		default:
			progress.Active++
		}
	}
	for _, status := range []string{"waiting", "ready_to_merge", "active"} {
		for _, c := range conversations {
			if len(progress.Actionable) == 3 || c.ParentConversationID == nil || c.Status != status {
				continue
			}
			progress.Actionable = append(progress.Actionable, map[string]any{
				"id": c.ID, "title": c.Title, "status": c.Status, "breadcrumb": conversationBreadcrumb(c, byID),
			})
		}
	}
	return progress
}

func conversationBreadcrumb(c Conversation, byID map[int64]Conversation) string {
	parts := []string{c.Title}
	for c.ParentConversationID != nil {
		parent, ok := byID[*c.ParentConversationID]
		if !ok {
			break
		}
		parts = append([]string{parent.Title}, parts...)
		c = parent
	}
	return strings.Join(parts, " / ")
}

func (a *App) createForks(w http.ResponseWriter, r *http.Request, parentID, jobID int64) {
	var input struct {
		ForkEventID int64    `json:"forkEventId"`
		Replies     []string `json:"replies"`
	}
	if decode(r, &input) != nil || input.ForkEventID < 1 || len(input.Replies) < 1 || len(input.Replies) > maxForksPerBatch {
		fail(w, http.StatusBadRequest, "provide a fork event and 1-10 replies")
		return
	}
	seen := map[string]bool{}
	for i, reply := range input.Replies {
		input.Replies[i] = strings.TrimSpace(reply)
		if input.Replies[i] == "" || len(input.Replies[i]) > 4000 {
			fail(w, http.StatusBadRequest, "each reply must be 1-4000 characters")
			return
		}
		key := strings.ToLower(input.Replies[i])
		if seen[key] {
			fail(w, http.StatusBadRequest, "duplicate replies are not allowed")
			return
		}
		seen[key] = true
	}
	var validEvent int
	if err := a.DB.QueryRow(`SELECT 1 FROM job_events WHERE id=? AND conversation_id=?`, input.ForkEventID, parentID).Scan(&validEvent); err != nil {
		fail(w, http.StatusConflict, "fork event is not in this conversation")
		return
	}
	workspaceID, sourceSessionID, err := a.conversationHermesSource(jobID, parentID)
	if err != nil {
		fail(w, http.StatusConflict, "parent Hermes session is unavailable")
		return
	}
	type remoteFork struct {
		session string
		title   string
		reply   string
	}
	remote := make([]remoteFork, 0, len(input.Replies))
	cleanup := func(forks []remoteFork) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, fork := range forks {
			_ = a.deleteHermesSession(ctx, workspaceID, fork.session)
		}
	}
	for _, reply := range input.Replies {
		title := conversationTitle(reply)
		sessionID, forkErr := a.forkHermesSession(r.Context(), workspaceID, sourceSessionID, token(), title)
		if forkErr != nil {
			cleanup(remote)
			fail(w, http.StatusBadGateway, forkErr.Error())
			return
		}
		remote = append(remote, remoteFork{session: sessionID, title: title, reply: reply})
	}
	tx, err := a.DB.Begin()
	if err != nil {
		cleanup(remote)
		fail(w, http.StatusInternalServerError, "could not create branches")
		return
	}
	defer tx.Rollback()
	created := []Conversation{}
	type pendingFork struct {
		id      int64
		session string
		message string
	}
	pending := []pendingFork{}
	for _, fork := range remote {
		result, txErr := tx.Exec(`INSERT INTO job_conversations(job_id,parent_conversation_id,fork_event_id,title,status,hermes_session_id)
			VALUES(?,?,?,?, 'active',?)`, jobID, parentID, input.ForkEventID, fork.title, fork.session)
		if txErr != nil {
			cleanup(remote)
			fail(w, http.StatusInternalServerError, "could not create branches")
			return
		}
		id, _ := result.LastInsertId()
		if txErr = appendConversationEventTx(tx, jobID, id, "comment", fork.reply); txErr != nil {
			cleanup(remote)
			fail(w, http.StatusInternalServerError, "could not create branches")
			return
		}
		var c Conversation
		var parent, forkEvent sql.NullInt64
		if txErr = tx.QueryRow(`SELECT id,job_id,parent_conversation_id,fork_event_id,title,status,created_at,updated_at FROM job_conversations WHERE id=?`, id).
			Scan(&c.ID, &c.JobID, &parent, &forkEvent, &c.Title, &c.Status, &c.CreatedAt, &c.UpdatedAt); txErr != nil {
			cleanup(remote)
			fail(w, http.StatusInternalServerError, "could not create branches")
			return
		}
		c.ParentConversationID, c.ForkEventID = &parent.Int64, &forkEvent.Int64
		created = append(created, c)
		pending = append(pending, pendingFork{id: id, session: fork.session, message: fork.reply})
	}
	if err = tx.Commit(); err != nil {
		cleanup(remote)
		fail(w, http.StatusInternalServerError, "could not create branches")
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{"conversations": created})
	a.signal()
	for _, fork := range pending {
		a.runConversationHermes(jobID, fork.id, fork.session, fork.message)
	}
}

func conversationTitle(reply string) string {
	titleRunes := []rune(reply)
	if len(titleRunes) <= 60 {
		return reply
	}
	return strings.TrimSpace(string(titleRunes[:57])) + "..."
}

func (a *App) conversationWorkspaceID(jobID int64) (int64, error) {
	var workspaceID int64
	err := a.DB.QueryRow(`SELECT w.id FROM jobs j
		JOIN workspaces w ON w.user_id=j.user_id
		LEFT JOIN columns c ON c.lane_id=j.lane_id
		LEFT JOIN boards b ON b.id=c.board_id
		WHERE j.id=? ORDER BY w.id=b.workspace_id DESC,w.id LIMIT 1`, jobID).Scan(&workspaceID)
	return workspaceID, err
}

func (a *App) conversationHermesSource(jobID, conversationID int64) (int64, string, error) {
	workspaceID, err := a.conversationWorkspaceID(jobID)
	if err != nil {
		return 0, "", err
	}
	var sessionID string
	var isMain bool
	if err = a.DB.QueryRow(`SELECT hermes_session_id,parent_conversation_id IS NULL
		FROM job_conversations WHERE id=? AND job_id=?`, conversationID, jobID).Scan(&sessionID, &isMain); err != nil {
		return 0, "", err
	}
	if isMain {
		var stored string
		if err = a.DB.QueryRow(`SELECT tmux_session FROM job_runs
			WHERE job_id=? AND tmux_session LIKE 'hermes-api:%' ORDER BY id DESC LIMIT 1`, jobID).Scan(&stored); err != nil {
			return 0, "", err
		}
		sessionID = strings.TrimPrefix(stored, "hermes-api:")
		if sessionID == "" {
			return 0, "", sql.ErrNoRows
		}
		if _, err = a.DB.Exec(`UPDATE job_conversations SET hermes_session_id=?,updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND hermes_session_id<>?`, sessionID, conversationID, sessionID); err != nil {
			return 0, "", err
		}
	}
	if sessionID == "" {
		return 0, "", sql.ErrNoRows
	}
	return workspaceID, sessionID, nil
}

func (a *App) hermesWorkspaceConfig(workspaceID int64) (string, string, error) {
	var base, key string
	err := a.DB.QueryRow(`SELECT hermes_url,hermes_api_key FROM workspaces WHERE id=?`, workspaceID).Scan(&base, &key)
	if err != nil {
		return "", "", err
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	if base == "" || key == "" {
		return "", "", fmt.Errorf("Hermes API is not configured")
	}
	return base, key, nil
}

func (a *App) forkHermesSession(ctx context.Context, workspaceID int64, sourceID, requestedID, title string) (string, error) {
	base, key, err := a.hermesWorkspaceConfig(workspaceID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"id": requestedID, "title": title})
	if err != nil {
		return "", err
	}
	endpoint := base + "/api/sessions/" + url.PathEscape(sourceID) + "/fork"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("Hermes fork error %d: %s", res.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var out struct {
		Object  string `json:"object"`
		Session struct {
			ID              string `json:"id"`
			ParentSessionID string `json:"parent_session_id"`
		} `json:"session"`
	}
	if err = json.Unmarshal(responseBody, &out); err != nil || out.Object != "hermes.session" ||
		out.Session.ID == "" || out.Session.ParentSessionID != sourceID {
		return "", fmt.Errorf("invalid Hermes fork response")
	}
	return out.Session.ID, nil
}

func (a *App) deleteHermesSession(ctx context.Context, workspaceID int64, sessionID string) error {
	base, key, err := a.hermesWorkspaceConfig(workspaceID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("Hermes delete error %d", res.StatusCode)
	}
	return nil
}

func (a *App) chatHermesSession(ctx context.Context, workspaceID int64, sessionID, message string) (string, error) {
	base, key, err := a.hermesWorkspaceConfig(workspaceID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/sessions/"+url.PathEscape(sessionID)+"/chat", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("Hermes chat error %d: %s", res.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var out struct {
		Object    string `json:"object"`
		SessionID string `json:"session_id"`
		Message   struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err = json.Unmarshal(responseBody, &out); err != nil || out.Object != "hermes.session.chat.completion" ||
		out.Message.Content == "" ||
		(out.SessionID != "" && out.SessionID != sessionID) {
		return "", fmt.Errorf("invalid Hermes chat response")
	}
	return out.Message.Content, nil
}

func (a *App) runConversationHermes(jobID, conversationID int64, sessionID, message string) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		workspaceID, err := a.conversationWorkspaceID(jobID)
		var reply string
		if err == nil {
			reply, err = a.chatHermesSession(context.Background(), workspaceID, sessionID, message)
		}
		kind, content, status := "reply", reply, "ready_to_merge"
		if err != nil {
			kind, content, status = "error", err.Error(), "waiting"
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-a.stop:
				cancel()
			case <-ctx.Done():
			}
		}()

		persistErr := a.persistConversationCompletionWithRetry(ctx, jobID, conversationID, sessionID, kind, content, status)
		if persistErr == nil || errors.Is(persistErr, errConversationCompletionStale) {
			return
		}
		fallback := fmt.Sprintf("Hermes completion could not be persisted: %v", persistErr)
		fallbackErr := a.persistConversationCompletion(ctx, jobID, conversationID, sessionID, "error", fallback, "waiting")
		if fallbackErr != nil && !errors.Is(fallbackErr, errConversationCompletionStale) {
			log.Printf("conversation %d Hermes completion persistence failed: %v; fallback failed: %v", conversationID, persistErr, fallbackErr)
		}
	}()
}

func (a *App) persistConversationCompletionWithRetry(ctx context.Context, jobID, conversationID int64, sessionID, kind, content, status string) error {
	var err error
	for attempt := 0; attempt < conversationCompletionAttempts; attempt++ {
		err = a.persistConversationCompletion(ctx, jobID, conversationID, sessionID, kind, content, status)
		if err == nil || errors.Is(err, errConversationCompletionStale) || !isSQLiteBusy(err) {
			return err
		}
		if attempt+1 == conversationCompletionAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(25*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func (a *App) persistConversationCompletion(ctx context.Context, jobID, conversationID int64, sessionID, kind, content, status string) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE job_conversations SET status=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND job_id=? AND status='active' AND hermes_session_id=?`,
		status, conversationID, jobID, sessionID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errConversationCompletionStale
	}
	if err = appendConversationEventTx(tx, jobID, conversationID, kind, content); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.signal()
	return nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

func (a *App) conversationEvents(w http.ResponseWriter, conversationID int64) {
	rows, err := a.DB.Query(`SELECT e.id,CASE WHEN e.kind='output' AND r.tmux_session LIKE 'hermes-api:%' THEN 'reply' ELSE e.kind END,e.content,e.created_at
		FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE e.conversation_id=? ORDER BY e.id`, conversationID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not load events")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, content, created string
		rows.Scan(&id, &kind, &content, &created)
		out = append(out, map[string]any{"id": id, "kind": kind, "content": content, "created_at": created})
	}
	jsonOut(w, http.StatusOK, out)
}

func (a *App) conversationComment(w http.ResponseWriter, r *http.Request, conversationID, jobID int64) {
	var input struct {
		Comment string `json:"comment"`
	}
	var attachments []jobAttachment
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var err error
		attachments, err = parseAttachments(w, r)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Comment = r.FormValue("comment")
	} else {
		if decode(r, &input) != nil {
			fail(w, http.StatusBadRequest, "invalid request")
			return
		}
	}
	input.Comment = strings.TrimSpace(input.Comment)
	if (input.Comment == "" && len(attachments) == 0) || len(input.Comment) > 4000 {
		fail(w, http.StatusBadRequest, "comment must be 1-4000 characters")
		return
	}
	input.Comment = appendAttachmentContext(input.Comment, attachments)
	tx, err := a.DB.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not record comment")
		return
	}
	defer tx.Rollback()
	var status, sessionID string
	if err = tx.QueryRow(`SELECT status,hermes_session_id FROM job_conversations WHERE id=?`, conversationID).Scan(&status, &sessionID); err != nil {
		fail(w, http.StatusInternalServerError, "could not record comment")
		return
	}
	if status == "active" {
		fail(w, http.StatusConflict, "conversation is already receiving an agent reply")
		return
	}
	if sessionID == "" {
		fail(w, http.StatusConflict, "conversation Hermes session is unavailable")
		return
	}
	if err = appendConversationEventTx(tx, jobID, conversationID, "comment", input.Comment); err == nil {
		_, err = tx.Exec(`UPDATE job_conversations SET status='active',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status<>'active'`,
			conversationID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not record comment")
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
	a.signal()
	a.runConversationHermes(jobID, conversationID, sessionID, input.Comment)
}

func (a *App) mergePreview(w http.ResponseWriter, sourceID int64) {
	var parentID int64
	var status string
	if err := a.DB.QueryRow(`SELECT parent_conversation_id,status FROM job_conversations WHERE id=?`, sourceID).Scan(&parentID, &status); err != nil {
		fail(w, http.StatusConflict, "Main cannot merge")
		return
	}
	if status == "active" {
		fail(w, http.StatusConflict, "conversation is still receiving an agent reply")
		return
	}
	var previous, watermark int64
	a.DB.QueryRow(`SELECT COALESCE(MAX(source_event_watermark),0) FROM conversation_merges WHERE source_conversation_id=?`, sourceID).Scan(&previous)
	a.DB.QueryRow(`SELECT COALESCE(MAX(id),0) FROM job_events WHERE conversation_id=?`, sourceID).Scan(&watermark)
	rows, err := a.DB.Query(`SELECT content FROM job_events WHERE conversation_id=? AND id>? AND kind IN('comment','input','reply','output') ORDER BY id`, sourceID, previous)
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not create merge preview")
		return
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var point string
		rows.Scan(&point)
		if point = mergePreviewPoint(point); point != "" {
			parts = append(parts, point)
		}
	}
	summary := strings.Join(parts, "\n\n")
	if len(summary) > maxMergeSummaryBytes {
		summary = summary[:maxMergeSummaryBytes]
		for len(summary) > 0 && !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
		summary = strings.TrimSpace(summary)
	}
	if summary == "" {
		fail(w, http.StatusConflict, "conversation has no new content to merge")
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"sourceConversationId": sourceID, "targetConversationId": parentID, "watermark": watermark, "summary": summary})
}

func mergePreviewPoint(content string) string {
	if attachment := strings.Index(content, "\n\nAdditional file context:"); attachment >= 0 {
		content = content[:attachment]
	}
	content = strings.TrimSpace(content)
	if len(content) <= maxMergePointBytes {
		return content
	}
	content = content[:maxMergePointBytes]
	for len(content) > 0 && !utf8.ValidString(content) {
		content = content[:len(content)-1]
	}
	return strings.TrimSpace(content)
}

func (a *App) mergeConversation(w http.ResponseWriter, r *http.Request, sourceID, jobID int64) {
	var input struct {
		Summary          string `json:"summary"`
		PreviewWatermark int64  `json:"previewWatermark"`
		IdempotencyKey   string `json:"idempotencyKey"`
	}
	if decode(r, &input) != nil {
		fail(w, http.StatusBadRequest, "invalid request")
		return
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.IdempotencyKey == "" || input.Summary == "" {
		fail(w, http.StatusBadRequest, "provide a summary and an idempotency key")
		return
	}
	if len(input.Summary) > maxMergeSummaryBytes {
		fail(w, http.StatusBadRequest, "summary must be 1-20000 characters")
		return
	}
	summaryJSON, _ := json.Marshal(input.Summary)
	if response, storedSummary, ok := a.existingMerge(sourceID, input.IdempotencyKey); ok {
		if storedSummary != input.Summary || response["watermark"] != input.PreviewWatermark {
			fail(w, http.StatusConflict, "idempotency key was already used for a different merge")
			return
		}
		jsonOut(w, http.StatusOK, response)
		return
	}
	if response, storedSummary, ok := a.existingMergeAtWatermark(sourceID, input.PreviewWatermark); ok {
		if storedSummary != input.Summary {
			fail(w, http.StatusConflict, "this conversation version was already merged with a different summary")
			return
		}
		jsonOut(w, http.StatusOK, response)
		return
	}
	tx, err := a.DB.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not merge conversation")
		return
	}
	defer tx.Rollback()
	var parentID int64
	var sourceTitle, author, status string
	if err = tx.QueryRow(`SELECT c.parent_conversation_id,c.title,u.email,c.status
		FROM job_conversations c JOIN jobs j ON j.id=c.job_id JOIN users u ON u.id=?
		WHERE c.id=? AND c.job_id=?`, uid(r), sourceID, jobID).
		Scan(&parentID, &sourceTitle, &author, &status); err != nil {
		fail(w, http.StatusConflict, "Main cannot merge")
		return
	}
	if status == "active" {
		fail(w, http.StatusConflict, "conversation is still receiving an agent reply")
		return
	}
	var watermark int64
	if err = tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM job_events WHERE conversation_id=?`, sourceID).Scan(&watermark); err != nil {
		fail(w, http.StatusInternalServerError, "could not merge conversation")
		return
	}
	if watermark != input.PreviewWatermark {
		fail(w, http.StatusConflict, "merge preview is stale; refresh it before confirming")
		return
	}
	result, err := tx.Exec(`INSERT INTO conversation_merges(source_conversation_id,target_conversation_id,approved_summary_json,source_event_watermark,idempotency_key,author_user_id)
		VALUES(?,?,?,?,?,?)`, sourceID, parentID, string(summaryJSON), watermark, input.IdempotencyKey, uid(r))
	if err != nil {
		tx.Rollback()
		if response, storedSummary, ok := a.existingMerge(sourceID, input.IdempotencyKey); ok && storedSummary == input.Summary && response["watermark"] == input.PreviewWatermark {
			jsonOut(w, http.StatusOK, response)
			return
		}
		if response, storedSummary, ok := a.existingMergeAtWatermark(sourceID, input.PreviewWatermark); ok && storedSummary == input.Summary {
			jsonOut(w, http.StatusOK, response)
			return
		}
		fail(w, http.StatusConflict, "could not merge conversation")
		return
	}
	var confirmedWatermark int64
	if err = tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM job_events WHERE conversation_id=?`, sourceID).Scan(&confirmedWatermark); err != nil {
		fail(w, http.StatusInternalServerError, "could not merge conversation")
		return
	}
	if confirmedWatermark != watermark {
		fail(w, http.StatusConflict, "merge preview is stale; refresh it before confirming")
		return
	}
	mergeID, _ := result.LastInsertId()
	var createdAt string
	tx.QueryRow(`SELECT created_at FROM conversation_merges WHERE id=?`, mergeID).Scan(&createdAt)
	card, _ := json.Marshal(map[string]any{
		"mergeId": mergeID, "sourceConversationId": sourceID, "sourceTitle": sourceTitle,
		"author": author, "createdAt": createdAt, "summary": input.Summary,
	})
	if err = appendConversationEventTx(tx, jobID, parentID, "merge", string(card)); err == nil {
		_, err = tx.Exec(`UPDATE job_conversations SET status='merged',updated_at=CURRENT_TIMESTAMP WHERE id=?`, sourceID)
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE job_conversations SET status='ready_to_merge',updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND parent_conversation_id IS NOT NULL`, parentID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not merge conversation")
		return
	}
	jsonOut(w, http.StatusCreated, mergeResponse(mergeID, sourceID, parentID, input.Summary, watermark, createdAt))
	a.signal()
}

func (a *App) existingMerge(sourceID int64, key string) (map[string]any, string, bool) {
	var id, target, watermark int64
	var summary, created string
	err := a.DB.QueryRow(`SELECT id,target_conversation_id,approved_summary_json,source_event_watermark,created_at
		FROM conversation_merges WHERE source_conversation_id=? AND idempotency_key=?`, sourceID, key).
		Scan(&id, &target, &summary, &watermark, &created)
	if err != nil {
		return nil, "", false
	}
	approved := approvedMergeSummary(summary)
	return mergeResponse(id, sourceID, target, approved, watermark, created), approved, true
}

func (a *App) existingMergeAtWatermark(sourceID, watermark int64) (map[string]any, string, bool) {
	var id, target int64
	var summary, created string
	err := a.DB.QueryRow(`SELECT id,target_conversation_id,approved_summary_json,created_at
		FROM conversation_merges WHERE source_conversation_id=? AND source_event_watermark=? ORDER BY id LIMIT 1`, sourceID, watermark).
		Scan(&id, &target, &summary, &created)
	if err != nil {
		return nil, "", false
	}
	approved := approvedMergeSummary(summary)
	return mergeResponse(id, sourceID, target, approved, watermark, created), approved, true
}

func approvedMergeSummary(stored string) string {
	var summary string
	if json.Unmarshal([]byte(stored), &summary) == nil {
		return summary
	}
	var points []string
	if json.Unmarshal([]byte(stored), &points) == nil {
		return strings.Join(points, "\n\n")
	}
	return strings.TrimSpace(stored)
}

func mergeResponse(id, source, target int64, summary string, watermark int64, created string) map[string]any {
	return map[string]any{"id": id, "sourceConversationId": source, "targetConversationId": target, "summary": summary, "watermark": watermark, "createdAt": created}
}

func (a *App) conversationStream(w http.ResponseWriter, r *http.Request, conversationID int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	var last int64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		rows, _ := a.DB.Query(`SELECT id,kind,content FROM job_events WHERE conversation_id=? AND id>? ORDER BY id`, conversationID, last)
		for rows.Next() {
			var kind, content string
			rows.Scan(&last, &kind, &content)
			payload, _ := json.Marshal(map[string]any{"id": last, "kind": kind, "content": content})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		rows.Close()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

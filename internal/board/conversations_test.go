package board

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func conversationFixture(t *testing.T) (*App, http.Handler, *http.Cookie, int64, int64) {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"branches@example.com","password":"password1"}`)
	var laneID int64
	if err = a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=(SELECT id FROM users WHERE email='branches@example.com')`).Scan(&laneID); err != nil {
		t.Fatal(err)
	}
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", laneID)
	w, _ := req(t, h, cookie, "POST", "/api/lanes/"+itoa(laneID)+"/jobs", `{"task":"Design branching","doneDefinition":"Branches can merge"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", w.Code, w.Body.String())
	}
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	jobID := int64(made["id"].(float64))
	if err = a.appendJobEvent(jobID, "reply", "Initial agent answer"); err != nil {
		t.Fatal(err)
	}
	var mainID int64
	if err = a.DB.QueryRow(`SELECT id FROM job_conversations WHERE job_id=? AND parent_conversation_id IS NULL`, jobID).Scan(&mainID); err != nil {
		t.Fatal(err)
	}
	return a, h, cookie, jobID, mainID
}

func waitForConversation(t *testing.T, a *App, conversationID int64, wantStatus string, wantReplies int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var replies int
		err := a.DB.QueryRow(`SELECT status FROM job_conversations WHERE id=?`, conversationID).Scan(&status)
		a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND kind='reply'`, conversationID).Scan(&replies)
		if err == nil && status == wantStatus && replies == wantReplies {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	var replies int
	a.DB.QueryRow(`SELECT status FROM job_conversations WHERE id=?`, conversationID).Scan(&status)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND kind='reply'`, conversationID).Scan(&replies)
	t.Fatalf("conversation %d status=%q replies=%d; want status=%q replies=%d", conversationID, status, replies, wantStatus, wantReplies)
}

func configureConversationHermes(t *testing.T, a *App, jobID int64, url string) {
	t.Helper()
	res, err := a.DB.Exec(`UPDATE workspaces SET hermes_url=?,hermes_api_key='secret',hermes_model='hermes-agent'
		WHERE user_id=(SELECT user_id FROM jobs WHERE id=?)`, url, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		t.Fatalf("configured %d workspaces", changed)
	}
}

func attachConversationProject(t *testing.T, a *App, jobID int64, directory string) {
	t.Helper()
	var userID, laneID, workspaceID int64
	if err := a.DB.QueryRow(`SELECT j.user_id,j.lane_id,w.id FROM jobs j JOIN workspaces w ON w.user_id=j.user_id WHERE j.id=? ORDER BY w.id LIMIT 1`, jobID).
		Scan(&userID, &laneID, &workspaceID); err != nil {
		t.Fatal(err)
	}
	project, err := a.DB.Exec(`INSERT INTO projects(user_id,workspace_id,name,directory) VALUES(?,?,'Branch Project',?)`, userID, workspaceID, directory)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	board, err := a.DB.Exec(`INSERT INTO boards(user_id,workspace_id,name) VALUES(?,?,'Branch Board')`, userID, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	boardID, _ := board.LastInsertId()
	if _, err = a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,name,position,project_id) VALUES(?,?,?,'Branch Column',0,?)`,
		userID, boardID, laneID, projectID); err != nil {
		t.Fatal(err)
	}
}

func TestForkRunsARealScopedHermesConversationAndReusesItsSession(t *testing.T) {
	var mu sync.Mutex
	var sessions, prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		mu.Lock()
		sessions = append(sessions, r.Header.Get("X-Hermes-Session-Id"))
		prompts = append(prompts, input.Messages[0].Content)
		call := len(prompts)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"agent reply ` + itoa(int64(call)) + `"}}]}`))
	}))
	defer server.Close()

	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	attachConversationProject(t, a, jobID, "/srv/branch-project")
	configureConversationHermes(t, a, jobID, server.URL)
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id LIMIT 1`, mainID).Scan(&forkEvent)
	if err := a.appendJobEvent(jobID, "reply", "Later parent answer must not leak"); err != nil {
		t.Fatal(err)
	}
	var mainRun, mainEvents int64
	a.DB.QueryRow(`SELECT id FROM job_runs WHERE job_id=? ORDER BY id DESC LIMIT 1`, jobID).Scan(&mainRun)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=?`, mainID).Scan(&mainEvents)

	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks",
		`{"forkEventId":`+itoa(forkEvent)+`,"replies":["Explore the branch"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Conversations []Conversation `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	childID := created.Conversations[0].ID
	waitForConversation(t, a, childID, "ready_to_merge", 1)

	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/comment", `{"comment":"Follow up in this fork"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("branch comment: %d %s", w.Code, w.Body.String())
	}
	waitForConversation(t, a, childID, "ready_to_merge", 2)

	mu.Lock()
	gotSessions := append([]string(nil), sessions...)
	gotPrompts := append([]string(nil), prompts...)
	mu.Unlock()
	if len(gotSessions) != 2 || gotSessions[0] == "" || gotSessions[0] != gotSessions[1] {
		t.Fatalf("Hermes sessions=%q", gotSessions)
	}
	if !strings.Contains(gotPrompts[0], "Initial agent answer") ||
		!strings.Contains(gotPrompts[0], "Explore the branch") ||
		!strings.Contains(gotPrompts[0], "/srv/branch-project") ||
		!strings.Contains(gotPrompts[0], "direct terminal tool") ||
		strings.Contains(gotPrompts[0], "Later parent answer must not leak") {
		t.Fatalf("fork prompt was not scoped to the selected event: %q", gotPrompts[0])
	}
	if gotPrompts[1] != "Follow up in this fork" {
		t.Fatalf("follow-up prompt=%q", gotPrompts[1])
	}
	var latestRun, mainEventsAfter int64
	a.DB.QueryRow(`SELECT id FROM job_runs WHERE job_id=? ORDER BY id DESC LIMIT 1`, jobID).Scan(&latestRun)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=?`, mainID).Scan(&mainEventsAfter)
	if latestRun != mainRun || mainEventsAfter != mainEvents {
		t.Fatalf("branch changed main run/timeline: run %d->%d events %d->%d", mainRun, latestRun, mainEvents, mainEventsAfter)
	}
}

func TestForkFailureBecomesWaitingInsteadOfFalselyActive(t *testing.T) {
	a, h, cookie, _, mainID := conversationFixture(t)
	defer a.Close()
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks",
		`{"forkEventId":`+itoa(forkEvent)+`,"replies":["Cannot execute"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Conversations []Conversation `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	childID := created.Conversations[0].ID
	waitForConversation(t, a, childID, "waiting", 0)
	var errors int
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND kind='error'`, childID).Scan(&errors)
	if errors != 1 {
		t.Fatalf("branch errors=%d", errors)
	}
}

func TestForkPromptBoundsLargeParentEvents(t *testing.T) {
	a, _, _, jobID, mainID := conversationFixture(t)
	defer a.Close()
	large := strings.Repeat("parent context ", 10000)
	if err := a.appendJobEvent(jobID, "reply", large); err != nil {
		t.Fatal(err)
	}
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	prompt, err := a.forkPrompt(jobID, mainID, forkEvent, "bounded opening")
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) > 70<<10 || !strings.Contains(prompt, "bounded opening") {
		t.Fatalf("fork prompt length=%d contains opening=%t", len(prompt), strings.Contains(prompt, "bounded opening"))
	}
}

func TestLegacyStoredForkStartsHermesWithItsExistingHistory(t *testing.T) {
	promptReceived := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		promptReceived <- input.Messages[0].Content
		w.Write([]byte(`{"choices":[{"message":{"content":"recovered reply"}}]}`))
	}))
	defer server.Close()
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	configureConversationHermes(t, a, jobID, server.URL)
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	result, err := a.DB.Exec(`INSERT INTO job_conversations(job_id,parent_conversation_id,fork_event_id,title,status)
		VALUES(?,?,?,'legacy stored fork','waiting')`, jobID, mainID, forkEvent)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := result.LastInsertId()
	tx, _ := a.DB.Begin()
	if err = appendConversationEventTx(tx, jobID, childID, "comment", "Legacy opening context"); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatal(err)
	}
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/comment", `{"comment":"Recover this branch"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("recover comment: %d %s", w.Code, w.Body.String())
	}
	waitForConversation(t, a, childID, "ready_to_merge", 1)
	select {
	case prompt := <-promptReceived:
		if !strings.Contains(prompt, "Legacy opening context") || !strings.Contains(prompt, "Recover this branch") {
			t.Fatalf("recovery prompt=%q", prompt)
		}
	default:
		t.Fatal("Hermes prompt was not captured")
	}
}

func TestActiveForkRejectsConcurrentCommentAndMergePreview(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	defer server.Close()
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	configureConversationHermes(t, a, jobID, server.URL)
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks",
		`{"forkEventId":`+itoa(forkEvent)+`,"replies":["Long request"]}`)
	var created struct {
		Conversations []Conversation `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	childID := created.Conversations[0].ID
	<-started
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/comment", `{"comment":"race"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("concurrent comment=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/merge-preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("active merge preview=%d %s", w.Code, w.Body.String())
	}
	close(release)
	waitForConversation(t, a, childID, "ready_to_merge", 1)
}

func TestConversationMigrationBackfillsExistingEventsIntoMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	a, err := Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"backfill@example.com","password":"password1"}`)
	var userID, laneID int64
	a.DB.QueryRow(`SELECT u.id,l.id FROM users u JOIN lanes l ON l.user_id=u.id WHERE u.email='backfill@example.com'`).Scan(&userID, &laneID)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", laneID)
	res, _ := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,?,0)`, userID, laneID, "legacy")
	jobID, _ := res.LastInsertId()
	run, _ := a.DB.Exec(`INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,0,'legacy','done')`, jobID)
	runID, _ := run.LastInsertId()
	a.DB.Exec(`INSERT INTO job_events(job_run_id,sequence,kind,content,conversation_id) VALUES(?,1,'output','legacy output',NULL)`, runID)
	a.Close()

	a, err = Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var title string
	var eventConversation, mainID int64
	if err = a.DB.QueryRow(`SELECT id,title FROM job_conversations WHERE job_id=? AND parent_conversation_id IS NULL`, jobID).Scan(&mainID, &title); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow(`SELECT conversation_id FROM job_events WHERE job_run_id=?`, runID).Scan(&eventConversation); err != nil {
		t.Fatal(err)
	}
	if title != "Main" || eventConversation != mainID {
		t.Fatalf("backfill title=%q event conversation=%d main=%d", title, eventConversation, mainID)
	}
	_ = cookie
}

func TestConversationMigrationMakesInterruptedActiveForksHonest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-db")
	a, err := Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"legacy-fork@example.com","password":"password1"}`)
	var laneID int64
	a.DB.QueryRow(`SELECT id FROM lanes WHERE user_id=(SELECT id FROM users WHERE email='legacy-fork@example.com')`).Scan(&laneID)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", laneID)
	w, _ := req(t, a.Handler(), cookie, "POST", "/api/lanes/"+itoa(laneID)+"/jobs", `{"task":"legacy fork"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	jobID := int64(made["id"].(float64))
	a.appendJobEvent(jobID, "reply", "parent")
	var mainID int64
	a.DB.QueryRow(`SELECT id FROM job_conversations WHERE job_id=? AND parent_conversation_id IS NULL`, jobID).Scan(&mainID)
	var eventID int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=?`, mainID).Scan(&eventID)
	result, err := a.DB.Exec(`INSERT INTO job_conversations(job_id,parent_conversation_id,fork_event_id,title,status,hermes_session_id)
		VALUES(?,?,?,'interrupted fork','active','interrupted-session')`, jobID, mainID, eventID)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := result.LastInsertId()
	a.Close()

	a, err = Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var status string
	a.DB.QueryRow(`SELECT status FROM job_conversations WHERE id=?`, childID).Scan(&status)
	if status != "waiting" {
		t.Fatalf("interrupted fork status=%q", status)
	}
	var broken sql.NullString
	if err = a.DB.QueryRow(`SELECT group_concat("table"||':'||rowid||':'||parent) FROM pragma_foreign_key_check`).Scan(&broken); err != nil {
		t.Fatal(err)
	}
	if broken.Valid {
		t.Fatalf("foreign key violations: %s", broken.String)
	}
}

func TestConversationTreeAndEventsAreAuthorizedAndScoped(t *testing.T) {
	a, h, owner, jobID, mainID := conversationFixture(t)
	defer a.Close()
	_, outsider := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"outsider-branches@example.com","password":"password1"}`)

	w, _ := req(t, h, owner, "GET", "/api/jobs/"+itoa(jobID)+"/conversations", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"title":"Main"`) {
		t.Fatalf("tree: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, outsider, "GET", "/api/jobs/"+itoa(jobID)+"/conversations", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider tree=%d %s", w.Code, w.Body.String())
	}

	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ = req(t, h, owner, "POST", "/api/conversations/"+itoa(mainID)+"/forks", `{"forkEventId":`+itoa(forkEvent)+`,"replies":["Explore SQLite"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	var forks struct {
		Conversations []struct {
			ID int64 `json:"id"`
		} `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &forks)
	childID := forks.Conversations[0].ID
	w, _ = req(t, h, owner, "GET", "/api/conversations/"+itoa(childID)+"/events", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Explore SQLite") || strings.Contains(w.Body.String(), "Initial agent answer") {
		t.Fatalf("scoped child events: %d %s", w.Code, w.Body.String())
	}
	streamRequest := httptest.NewRequest(http.MethodGet, "/api/conversations/"+itoa(childID)+"/stream", nil)
	streamRequest.AddCookie(owner)
	streamContext, cancelStream := context.WithCancel(streamRequest.Context())
	cancelStream()
	streamResponse := httptest.NewRecorder()
	h.ServeHTTP(streamResponse, streamRequest.WithContext(streamContext))
	if streamResponse.Code != http.StatusOK || !strings.Contains(streamResponse.Body.String(), "Explore SQLite") || strings.Contains(streamResponse.Body.String(), "Initial agent answer") {
		t.Fatalf("scoped child stream: %d %s", streamResponse.Code, streamResponse.Body.String())
	}
	w, _ = req(t, h, outsider, "GET", "/api/conversations/"+itoa(childID)+"/events", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider events=%d", w.Code)
	}
	w, _ = req(t, h, owner, "GET", "/api/jobs/"+itoa(jobID), "")
	var detail struct {
		Events []struct {
			Content string `json:"content"`
		} `json:"events"`
	}
	json.Unmarshal(w.Body.Bytes(), &detail)
	timeline, _ := json.Marshal(detail.Events)
	if strings.Contains(string(timeline), "Explore SQLite") || !strings.Contains(string(timeline), "Initial agent answer") {
		t.Fatalf("compact job timeline leaked fork events: %s", w.Body.String())
	}
}

func TestBatchForksAreAtomicAndCreateSiblings(t *testing.T) {
	a, h, cookie, _, mainID := conversationFixture(t)
	defer a.Close()
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)

	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks", `{"forkEventId":`+itoa(forkEvent)+`,"replies":["First option","Second option"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("batch fork: %d %s", w.Code, w.Body.String())
	}
	var children, openingEvents int
	a.DB.QueryRow(`SELECT count(*) FROM job_conversations WHERE parent_conversation_id=?`, mainID).Scan(&children)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id IN (SELECT id FROM job_conversations WHERE parent_conversation_id=?)`, mainID).Scan(&openingEvents)
	if children != 2 || openingEvents != 2 {
		t.Fatalf("children=%d opening events=%d", children, openingEvents)
	}

	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks", `{"forkEventId":`+itoa(forkEvent)+`,"replies":["valid","   "]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid batch=%d %s", w.Code, w.Body.String())
	}
	a.DB.QueryRow(`SELECT count(*) FROM job_conversations WHERE parent_conversation_id=?`, mainID).Scan(&children)
	if children != 2 {
		t.Fatalf("invalid batch partially created children: %d", children)
	}
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks", `{"forkEventId":`+itoa(forkEvent)+`,"replies":["same","same"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate replies=%d", w.Code)
	}
}

func TestMergeUsesDirectParentAndProtectsStaleAndDuplicateConfirmation(t *testing.T) {
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	fork := func(parent int64, reply string) int64 {
		t.Helper()
		var eventID int64
		a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, parent).Scan(&eventID)
		w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(parent)+"/forks", `{"forkEventId":`+itoa(eventID)+`,"replies":["`+reply+`"]}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("fork %q: %d %s", reply, w.Code, w.Body.String())
		}
		var out struct {
			Conversations []struct {
				ID int64 `json:"id"`
			} `json:"conversations"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		a.DB.Exec(`UPDATE job_conversations SET status='ready_to_merge' WHERE id=?`, out.Conversations[0].ID)
		return out.Conversations[0].ID
	}
	childID := fork(mainID, "Child insight")
	grandchildID := fork(childID, "Nested insight")

	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge-preview", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		Watermark int64    `json:"watermark"`
		Points    []string `json:"points"`
	}
	json.Unmarshal(w.Body.Bytes(), &preview)
	if len(preview.Points) == 0 {
		t.Fatal("preview omitted important points")
	}
	tx, err := a.DB.Begin()
	if err == nil {
		err = appendConversationEventTx(tx, jobID, grandchildID, "comment", "newer point")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatalf("newer point: %v", err)
	}
	a.DB.Exec(`UPDATE job_conversations SET status='ready_to_merge' WHERE id=?`, grandchildID)
	mergeBody := `{"points":["Reviewed nested point"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"merge-1"}`
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", mergeBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale merge=%d %s", w.Code, w.Body.String())
	}

	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge-preview", `{}`)
	json.Unmarshal(w.Body.Bytes(), &preview)
	mergeBody = `{"points":["Reviewed nested point"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"merge-2"}`
	a.DB.Exec(`UPDATE job_conversations SET status='merged' WHERE id=?`, childID)
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", mergeBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	first := w.Body.String()
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", mergeBody)
	if w.Code != http.StatusOK || w.Body.String() != first {
		t.Fatalf("idempotent merge: %d first=%s duplicate=%s", w.Code, first, w.Body.String())
	}
	differentBody := `{"points":["Different reviewed point"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"merge-2"}`
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", differentBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("reused idempotency key with different payload=%d %s", w.Code, w.Body.String())
	}
	newKeySameMerge := `{"points":["Reviewed nested point"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"merge-3"}`
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", newKeySameMerge)
	if w.Code != http.StatusOK || w.Body.String() != first {
		t.Fatalf("same watermark duplicate: %d first=%s duplicate=%s", w.Code, first, w.Body.String())
	}
	newKeyDifferentMerge := `{"points":["Changed after confirmation"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"merge-4"}`
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(grandchildID)+"/merge", newKeyDifferentMerge)
	if w.Code != http.StatusConflict {
		t.Fatalf("same watermark with different content=%d %s", w.Code, w.Body.String())
	}
	var targetID int64
	var parentStatus string
	var summaries, rawNested int
	a.DB.QueryRow(`SELECT target_conversation_id FROM conversation_merges WHERE source_conversation_id=?`, grandchildID).Scan(&targetID)
	a.DB.QueryRow(`SELECT status FROM job_conversations WHERE id=?`, childID).Scan(&parentStatus)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND kind='merge'`, childID).Scan(&summaries)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND kind<>'merge' AND content LIKE '%Nested insight%'`, childID).Scan(&rawNested)
	if targetID != childID || parentStatus != "ready_to_merge" || summaries != 1 || rawNested != 0 {
		t.Fatalf("target=%d child=%d parent status=%q summaries=%d raw nested=%d", targetID, childID, parentStatus, summaries, rawNested)
	}
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/merge-preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("main preview=%d", w.Code)
	}
}

func TestMergeDetectsAnEventRacingAfterThePreviewCheck(t *testing.T) {
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks",
		`{"forkEventId":`+itoa(forkEvent)+`,"replies":["Race branch"]}`)
	var made struct {
		Conversations []Conversation `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &made)
	childID := made.Conversations[0].ID
	a.DB.Exec(`UPDATE job_conversations SET status='ready_to_merge' WHERE id=?`, childID)
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/merge-preview", `{}`)
	var preview struct {
		Watermark int64 `json:"watermark"`
	}
	json.Unmarshal(w.Body.Bytes(), &preview)
	var runID int64
	a.DB.QueryRow(`SELECT id FROM job_runs WHERE job_id=? ORDER BY id DESC LIMIT 1`, jobID).Scan(&runID)
	_, err := a.DB.Exec(`CREATE TRIGGER race_merge_event BEFORE INSERT ON conversation_merges BEGIN
		INSERT INTO job_events(job_run_id,sequence,kind,content,conversation_id)
		VALUES(` + itoa(runID) + `,(SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_run_id=` + itoa(runID) + `),'comment','racing point',` + itoa(childID) + `);
	END`)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"points":["Reviewed"],"previewWatermark":` + itoa(preview.Watermark) + `,"idempotencyKey":"race"}`
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/merge", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("racing merge=%d %s", w.Code, w.Body.String())
	}
	var merges, racingEvents int
	a.DB.QueryRow(`SELECT count(*) FROM conversation_merges WHERE source_conversation_id=?`, childID).Scan(&merges)
	a.DB.QueryRow(`SELECT count(*) FROM job_events WHERE conversation_id=? AND content='racing point'`, childID).Scan(&racingEvents)
	if merges != 0 || racingEvents != 0 {
		t.Fatalf("racing merge was not atomic: merges=%d racingEvents=%d", merges, racingEvents)
	}
}

func TestConversationRelationshipsAreProtectedByDatabaseIntegrity(t *testing.T) {
	a, _, _, jobID, mainID := conversationFixture(t)
	defer a.Close()
	var userID, laneID int64
	a.DB.QueryRow(`SELECT user_id,lane_id FROM jobs WHERE id=?`, jobID).Scan(&userID, &laneID)
	result, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,?,99)`, userID, laneID, "other job")
	if err != nil {
		t.Fatal(err)
	}
	otherJob, _ := result.LastInsertId()
	a.DB.Exec(`INSERT INTO job_conversations(job_id,title,status) VALUES(?,'Main','active')`, otherJob)
	var otherMain int64
	a.DB.QueryRow(`SELECT id FROM job_conversations WHERE job_id=?`, otherJob).Scan(&otherMain)
	if _, err = a.DB.Exec(`INSERT INTO job_conversations(job_id,parent_conversation_id,title,status) VALUES(?,?,'cross-job','waiting')`, jobID, otherMain); err == nil {
		t.Fatal("cross-job parent was accepted")
	}
	var eventID int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? LIMIT 1`, mainID).Scan(&eventID)
	childResult, err := a.DB.Exec(`INSERT INTO job_conversations(job_id,parent_conversation_id,fork_event_id,title,status) VALUES(?,?,?,'child','ready_to_merge')`, jobID, mainID, eventID)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := childResult.LastInsertId()
	if _, err = a.DB.Exec(`INSERT INTO conversation_merges(source_conversation_id,target_conversation_id,approved_summary_json,source_event_watermark,idempotency_key,author_user_id)
		VALUES(?,?, '["skip"]',?,'skip-parent',?)`, childID, otherMain, eventID, userID); err == nil {
		t.Fatal("non-parent merge target was accepted")
	}
}

func TestConversationProgressCountsResolvedAndLimitsActionableRows(t *testing.T) {
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks",
		`{"forkEventId":`+itoa(forkEvent)+`,"replies":["one","two","three","four","five"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("forks: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Conversations []struct {
			ID int64 `json:"id"`
		} `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	ids := []int64{}
	for _, conversation := range out.Conversations {
		ids = append(ids, conversation.ID)
	}
	a.DB.Exec(`UPDATE job_conversations SET status='waiting' WHERE id=?`, ids[1])
	a.DB.Exec(`UPDATE job_conversations SET status='ready_to_merge' WHERE id=?`, ids[2])
	a.DB.Exec(`UPDATE job_conversations SET status='waiting' WHERE id=?`, ids[3])
	var watermark, userID int64
	a.DB.QueryRow(`SELECT max(id) FROM job_events WHERE conversation_id=?`, ids[0]).Scan(&watermark)
	a.DB.QueryRow(`SELECT user_id FROM jobs WHERE id=?`, jobID).Scan(&userID)
	a.DB.Exec(`INSERT INTO conversation_merges(source_conversation_id,target_conversation_id,approved_summary_json,source_event_watermark,idempotency_key,author_user_id)
		VALUES(?,?,'["done"]',?,'progress-test',?)`, ids[0], mainID, watermark, userID)
	a.DB.Exec(`UPDATE job_conversations SET status='merged' WHERE id=?`, ids[0])

	w, _ = req(t, h, cookie, "GET", "/api/jobs/"+itoa(jobID)+"/conversations", "")
	var tree struct {
		Progress ConversationProgress `json:"progress"`
	}
	json.Unmarshal(w.Body.Bytes(), &tree)
	if tree.Progress.Total != 5 || tree.Progress.Resolved != 1 || tree.Progress.Merged != 1 ||
		tree.Progress.Waiting != 2 || tree.Progress.ReadyToMerge != 1 || tree.Progress.Active != 1 ||
		len(tree.Progress.Actionable) != 3 {
		t.Fatalf("progress=%+v body=%s", tree.Progress, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/lanes", "")
	if !strings.Contains(w.Body.String(), `"conversationProgress":{"total":5,"resolved":1`) {
		t.Fatalf("job card progress missing: %s", w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/jobs/"+itoa(jobID), "")
	if !strings.Contains(w.Body.String(), `"conversationProgress":{"total":5,"resolved":1`) {
		t.Fatalf("job detail progress missing: %s", w.Body.String())
	}
}

func TestConversationCommentAcceptsAttachments(t *testing.T) {
	a, h, cookie, _, mainID := conversationFixture(t)
	defer a.Close()
	var forkEvent int64
	a.DB.QueryRow(`SELECT id FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, mainID).Scan(&forkEvent)
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/forks", `{"forkEventId":`+itoa(forkEvent)+`,"replies":["Attachment branch"]}`)
	var forked struct {
		Conversations []struct {
			ID int64 `json:"id"`
		} `json:"conversations"`
	}
	json.Unmarshal(w.Body.Bytes(), &forked)
	childID := forked.Conversations[0].ID
	waitForConversation(t, a, childID, "waiting", 0)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("comment", "Review this")
	part, err := form.CreateFormFile("files", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("branch context"))
	form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/conversations/"+itoa(childID)+"/comment", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("multipart comment: %d %s", response.Code, response.Body.String())
	}
	var content string
	a.DB.QueryRow(`SELECT content FROM job_events WHERE conversation_id=? ORDER BY id DESC LIMIT 1`, childID).Scan(&content)
	if !strings.Contains(content, "Review this") || !strings.Contains(content, "notes.txt") || !strings.Contains(content, "branch context") {
		t.Fatalf("attachment context missing: %q", content)
	}
	waitForConversation(t, a, childID, "waiting", 0)
	response, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(childID)+"/merge-preview", `{}`)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "branch context") || strings.Contains(response.Body.String(), "Attached file:") {
		t.Fatalf("merge preview exposed raw attachment content: %d %s", response.Code, response.Body.String())
	}
}

func TestMainConversationPreservesJobCommentStateAndArchivedConversationsAreReadOnly(t *testing.T) {
	a, h, cookie, jobID, mainID := conversationFixture(t)
	defer a.Close()
	w, _ := req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/comment", `{"comment":"too early"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("todo Main comment bypassed job state: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "DELETE", "/api/jobs/"+itoa(jobID), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/jobs/"+itoa(jobID)+"/conversations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("archived tree unreadable: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "POST", "/api/conversations/"+itoa(mainID)+"/comment", `{"comment":"after archive"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("archived mutation=%d %s", w.Code, w.Body.String())
	}
}

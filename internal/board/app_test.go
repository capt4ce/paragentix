package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobEventSourceMessageKeyMigration(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	var sourceMessageKeyColumn int
	if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('job_events') WHERE name='source_message_key'`).Scan(&sourceMessageKeyColumn); err != nil {
		t.Fatal(err)
	}
	if sourceMessageKeyColumn != 1 {
		t.Fatalf("source_message_key columns=%d, want 1", sourceMessageKeyColumn)
	}

	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"event-key@example.com","password":"password1"}`)
	var user, lane int64
	if err = a.DB.QueryRow("SELECT id FROM users WHERE email='event-key@example.com'").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,'work',0)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'hermes-api:test','running')", job)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()
	if _, err = a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content,source_message_key) VALUES(?,1,'intermediary','first','message-1')", run); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content,source_message_key) VALUES(?,2,'intermediary','duplicate','message-1')", run); err == nil {
		t.Fatal("duplicate source_message_key in one run was inserted")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate insert error=%v, want unique constraint", err)
	}
}

func TestNormalizeHermesMessages(t *testing.T) {
	tests := []struct {
		name             string
		messages         []hermesMessage
		wantIntermediate []string
		wantFinal        string
	}{
		{
			name: "assistant commentary is retained and final is reserved",
			messages: []hermesMessage{
				{Role: "user", Content: "work"},
				{ID: "progress-1", Role: "assistant", Content: "Delegating implementation"},
				{Role: "tool", Content: `{"secret":"tool result"}`},
				{ID: "final-1", Role: "assistant", Content: "Finished safely"},
			},
			wantIntermediate: []string{"Delegating implementation"},
			wantFinal:        "Finished safely",
		},
		{
			name: "reasoning attached to tool calls is retained without tool payloads",
			messages: []hermesMessage{
				{ID: "progress-1", Role: "assistant", Content: "", Reasoning: "**Inspecting project state**", ToolCalls: json.RawMessage(`[{"function":{"name":"terminal","arguments":"{\\"command\\":\\"secret\\"}"}}]`)},
				{Role: "tool", Content: `{"secret":"tool result"}`},
				{ID: "final-1", Role: "assistant", Content: "Finished safely"},
			},
			wantIntermediate: []string{"Inspecting project state"},
			wantFinal:        "Finished safely",
		},
		{
			name: "unsafe and empty rows are skipped",
			messages: []hermesMessage{
				{Role: "assistant", Content: map[string]any{"tool_calls": []any{"raw arguments"}}},
				{Role: "assistant", Content: "   "},
				{Role: "user", Content: "private user text"},
				{Role: "tool", Content: "private tool text"},
				{Role: "assistant", Content: "Visible final"},
			},
			wantFinal: "Visible final",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intermediates, final := normalizeHermesMessages(tc.messages)
			var got []string
			for _, message := range intermediates {
				got = append(got, message.Content)
			}
			if strings.Join(got, "|") != strings.Join(tc.wantIntermediate, "|") {
				t.Fatalf("intermediates=%v, want %v", got, tc.wantIntermediate)
			}
			if final != tc.wantFinal {
				t.Fatalf("final=%q, want %q", final, tc.wantFinal)
			}
		})
	}
}

func TestNormalizeHermesMessagesUsesStableSourceKeys(t *testing.T) {
	messages := []hermesMessage{
		{ID: "provider-message", Role: "assistant", Content: "With provider identity"},
		{Role: "assistant", Content: "With fallback identity"},
		{Role: "assistant", Content: "Final response"},
	}
	first, _ := normalizeHermesMessages(messages)
	second, _ := normalizeHermesMessages(messages)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("normalized lengths=%d,%d, want 2,2", len(first), len(second))
	}
	if first[0].SourceKey != "provider-message" {
		t.Fatalf("provider source key=%q", first[0].SourceKey)
	}
	if first[1].SourceKey == "" || first[1].SourceKey != second[1].SourceKey {
		t.Fatalf("fallback source keys=%q,%q, want equal non-empty keys", first[1].SourceKey, second[1].SourceKey)
	}
}

func TestRunHermesJobPersistsRepeatedIntermediaryOnceBeforeReply(t *testing.T) {
	var messageRequests atomic.Int32
	var a *App
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var intermediary int
				if a != nil {
					_ = a.DB.QueryRow("SELECT count(*) FROM job_events WHERE kind='intermediary' AND content='Delegating implementation'").Scan(&intermediary)
				}
				if intermediary == 1 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"Final response"}}]}`))
		case "/api/sessions/active-session/messages":
			messageRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"progress-1","role":"assistant","content":"Delegating implementation"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var err error
	a, err = Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"active-progress@example.com","password":"password1"}`)
	var user, lane int64
	if err = a.DB.QueryRow("SELECT id FROM users WHERE email='active-progress@example.com'").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=? ORDER BY id LIMIT 1", user).Scan(&lane); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Lane 1',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position,attempt_count) VALUES(?,?,'work','in_progress',0,1)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'hermes-api:active-session','running')", job)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()

	a.runHermesJob(job, run, "active-session", "work")

	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		if err = a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "in_review" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%q, message requests=%d, want in_review", state, messageRequests.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	rows, err := a.DB.Query("SELECT kind,content FROM job_events WHERE job_run_id=? AND kind IN ('intermediary','reply') ORDER BY sequence", run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind, content string
		if err = rows.Scan(&kind, &content); err != nil {
			t.Fatal(err)
		}
		got = append(got, kind+":"+content)
	}
	if strings.Join(got, "|") != "intermediary:Delegating implementation|reply:Final response" {
		t.Fatalf("events=%v", got)
	}
	if messageRequests.Load() < 1 {
		t.Fatalf("message requests=%d, want active session polling", messageRequests.Load())
	}
}

func TestCreateBoardJobCreatesColumnAtomically(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"atomic@example.com","password":"password1"}`)
	var boardID, projectID int64
	if err = a.DB.QueryRow(`SELECT b.id,p.id FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=(SELECT id FROM users WHERE email=?)`, "atomic@example.com").Scan(&boardID, &projectID); err != nil {
		t.Fatal(err)
	}

	w, _ := req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/jobs", `{"projectId":`+itoa(projectID)+`,"task":"Parallel work","doneDefinition":"Verified"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create board job: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	columnID := int64(out["columnId"].(float64))
	var count int
	if err = a.DB.QueryRow(`SELECT count(*) FROM jobs j JOIN columns c ON c.lane_id=j.lane_id WHERE c.id=? AND c.project_id=? AND j.task='Parallel work'`, columnID, projectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("created graph count=%d err=%v", count, err)
	}

	w, _ = req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/jobs", `{"projectId":999999,"task":"Must rollback"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid project: %d %s", w.Code, w.Body.String())
	}
	if err = a.DB.QueryRow(`SELECT count(*) FROM jobs WHERE task='Must rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial job count=%d err=%v", count, err)
	}
}

func TestCreateJobWithAttachmentsPersistsPromptContext(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"attachments@example.com","password":"password1"}`)
	var lane int64
	if err = a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=(SELECT id FROM users WHERE email='attachments@example.com')").Scan(&lane); err != nil {
		t.Fatal(err)
	}
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("task", "Review these notes")
	form.WriteField("doneDefinition", "Summarize both files")
	for name, content := range map[string]string{"notes.txt": "first note", "config.json": `{"enabled":true}`, "image.bin": string([]byte{0xff, 0x00, 0x01})} {
		part, partErr := form.CreateFormFile("files", name)
		if partErr != nil {
			t.Fatal(partErr)
		}
		part.Write([]byte(content))
	}
	form.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/lanes/"+itoa(lane)+"/jobs", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	job := int64(out["id"].(float64))
	rows, err := a.DB.Query("SELECT name,content FROM job_attachments WHERE job_id=? ORDER BY name", job)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var attachments []jobAttachment
	for rows.Next() {
		var attachment jobAttachment
		if err = rows.Scan(&attachment.Name, &attachment.Content); err != nil {
			t.Fatal(err)
		}
		attachments = append(attachments, attachment)
	}
	prompt := initialHermesPrompt("Project", "/project", "Review these notes", "Summarize both files", attachments)
	for _, want := range []string{"Attached file: config.json\n```\n{\"enabled\":true}\n```", "Attached file: notes.txt\n```\nfirst note\n```", "Attached file: image.bin\n```\nBase64-encoded content:\n/wAB\n```"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestMoveTodoJobToEndOfSameProjectColumn(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"move-job@example.com","password":"password1"}`)
	var boardID, projectID int64
	a.DB.QueryRow(`SELECT b.id,p.id FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=(SELECT id FROM users WHERE email=?)`, "move-job@example.com").Scan(&boardID, &projectID)
	columns, lanes := make([]int64, 2), make([]int64, 2)
	for i, name := range []string{"Parallel", "Sync"} {
		w, _ := req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"`+name+`","projectId":`+itoa(projectID)+`}`)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		columns[i] = int64(out["id"].(float64))
		a.DB.QueryRow("SELECT lane_id FROM columns WHERE id=?", columns[i]).Scan(&lanes[i])
	}
	a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES((SELECT id FROM users WHERE email=?),?,'existing','done',4)", "move-job@example.com", lanes[1])
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES((SELECT id FROM users WHERE email=?),?,'move me','todo',0)", "move-job@example.com", lanes[0])
	jobID, _ := res.LastInsertId()
	w, _ := req(t, h, cookie, "POST", "/api/jobs/"+itoa(jobID)+"/move", `{"columnId":`+itoa(columns[1])+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	var laneID int64
	var position int
	a.DB.QueryRow("SELECT lane_id,position FROM jobs WHERE id=?", jobID).Scan(&laneID, &position)
	if laneID != lanes[1] || position != 5 {
		t.Fatalf("moved lane=%d position=%d", laneID, position)
	}
}

func TestMoveJobCreatesColumnAndReturnsItsID(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"move-new@example.com","password":"password1"}`)
	var boardID, projectID, laneID int64
	a.DB.QueryRow(`SELECT b.id,p.id FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=(SELECT id FROM users WHERE email=?)`, "move-new@example.com").Scan(&boardID, &projectID)
	w, _ := req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"Source","projectId":`+itoa(projectID)+`}`)
	var column map[string]any
	json.Unmarshal(w.Body.Bytes(), &column)
	a.DB.QueryRow("SELECT lane_id FROM columns WHERE id=?", int64(column["id"].(float64))).Scan(&laneID)
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES((SELECT id FROM users WHERE email=?),?,'new destination','in_progress',0)", "move-new@example.com", laneID)
	jobID, _ := res.LastInsertId()
	w, _ = req(t, h, cookie, "POST", "/api/jobs/"+itoa(jobID)+"/move", `{"newColumnName":"Created destination"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	newColumnID := int64(out["columnId"].(float64))
	if newColumnID == 0 {
		t.Fatal("move returned no created column id")
	}
	var movedLane int64
	a.DB.QueryRow("SELECT lane_id FROM jobs WHERE id=?", jobID).Scan(&movedLane)
	if movedLane == laneID {
		t.Fatal("job remained in source column")
	}
}

func TestReorderColumnsPersistsBoardOrder(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"reorder@example.com","password":"password1"}`)

	var boardID, projectID int64
	if err = a.DB.QueryRow(`SELECT b.id, p.id FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=(SELECT id FROM users WHERE email=?)`, "reorder@example.com").Scan(&boardID, &projectID); err != nil {
		t.Fatal(err)
	}
	columnIDs := make([]int64, 0, 4)
	for _, name := range []string{"Archived", "Todo", "Doing", "Done"} {
		w, _ := req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"`+name+`","projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		var column map[string]any
		json.Unmarshal(w.Body.Bytes(), &column)
		columnIDs = append(columnIDs, int64(column["id"].(float64)))
	}
	w, _ := req(t, h, cookie, "DELETE", "/api/columns/"+itoa(columnIDs[0]), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive column: %d %s", w.Code, w.Body.String())
	}
	columnIDs = columnIDs[1:]

	w, _ = req(t, h, cookie, "PATCH", "/api/boards/"+itoa(boardID)+"/columns", `{"columnIds":[`+itoa(columnIDs[2])+`,`+itoa(columnIDs[0])+`,`+itoa(columnIDs[1])+`]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/boards/"+itoa(boardID)+"/columns", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get columns: %d %s", w.Code, w.Body.String())
	}
	var columns []map[string]any
	json.Unmarshal(w.Body.Bytes(), &columns)
	got := []string{columns[0]["name"].(string), columns[1]["name"].(string), columns[2]["name"].(string)}
	if strings.Join(got, ",") != "Done,Todo,Doing" {
		t.Fatalf("persisted order = %v", got)
	}
}

func TestArchiveColumnArchivesItsJobsAtomically(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"archive-column@example.com","password":"password1"}`)

	var userID, boardID, projectID int64
	if err = a.DB.QueryRow(`SELECT u.id,b.id,p.id FROM users u JOIN boards b ON b.user_id=u.id JOIN projects p ON p.workspace_id=b.workspace_id WHERE u.email=?`, "archive-column@example.com").Scan(&userID, &boardID, &projectID); err != nil {
		t.Fatal(err)
	}
	createColumn := func(name string) (int64, int64) {
		t.Helper()
		w, _ := req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"`+name+`","projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create column: %d %s", w.Code, w.Body.String())
		}
		var column map[string]any
		json.Unmarshal(w.Body.Bytes(), &column)
		columnID := int64(column["id"].(float64))
		var laneID int64
		if err := a.DB.QueryRow(`SELECT lane_id FROM columns WHERE id=?`, columnID).Scan(&laneID); err != nil {
			t.Fatal(err)
		}
		return columnID, laneID
	}
	insertJob := func(laneID int64, position int) int64 {
		t.Helper()
		res, err := a.DB.Exec(`INSERT INTO jobs(user_id,lane_id,task,position) VALUES(?,?,?,?)`, userID, laneID, "job", position)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	columnID, laneID := createColumn("Archive all")
	jobIDs := []int64{insertJob(laneID, 0), insertJob(laneID, 1)}
	w, _ := req(t, h, cookie, "DELETE", "/api/columns/"+itoa(columnID), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive column: %d %s", w.Code, w.Body.String())
	}
	for _, jobID := range jobIDs {
		var archived bool
		if err = a.DB.QueryRow(`SELECT archived FROM jobs WHERE id=?`, jobID).Scan(&archived); err != nil || !archived {
			t.Fatalf("job %d archived=%t err=%v", jobID, archived, err)
		}
	}

	columnID, laneID = createColumn("Rollback all")
	jobID := insertJob(laneID, 0)
	if _, err = a.DB.Exec(`CREATE TRIGGER reject_job_archive BEFORE UPDATE OF archived ON jobs WHEN OLD.id=` + itoa(jobID) + ` AND NEW.archived=1 BEGIN SELECT RAISE(ABORT, 'reject archive'); END`); err != nil {
		t.Fatal(err)
	}
	w, _ = req(t, h, cookie, "DELETE", "/api/columns/"+itoa(columnID), "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("archive with job failure: %d %s", w.Code, w.Body.String())
	}
	var columnArchived, jobArchived bool
	if err = a.DB.QueryRow(`SELECT c.archived,j.archived FROM columns c JOIN jobs j ON j.lane_id=c.lane_id WHERE c.id=? AND j.id=?`, columnID, jobID).Scan(&columnArchived, &jobArchived); err != nil {
		t.Fatal(err)
	}
	if columnArchived || jobArchived {
		t.Fatalf("archive was not atomic: column=%t job=%t", columnArchived, jobArchived)
	}
}

func TestCreateJobRejectsOversizedAttachmentsWithoutCreatingJob(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"invalid-attachment@example.com","password":"password1"}`)
	var lane int64
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=(SELECT id FROM users WHERE email='invalid-attachment@example.com')").Scan(&lane)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("task", "Do not persist")
	part, _ := form.CreateFormFile("files", "large.dat")
	part.Write(make([]byte, maxAttachmentSize+1))
	form.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/lanes/"+itoa(lane)+"/jobs", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "20 MB") {
		t.Fatalf("oversized attachment: %d %s", w.Code, w.Body.String())
	}
	var jobs int
	a.DB.QueryRow("SELECT count(*) FROM jobs WHERE lane_id=?", lane).Scan(&jobs)
	if jobs != 0 {
		t.Fatalf("invalid upload created %d jobs", jobs)
	}
}

func TestCreateJobRejectsTooManyAttachments(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, cookie := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"many-attachments@example.com","password":"password1"}`)
	var lane int64
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=(SELECT id FROM users WHERE email='many-attachments@example.com')").Scan(&lane)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("task", "Do not persist")
	for i := 0; i < maxAttachments+1; i++ {
		part, _ := form.CreateFormFile("files", fmt.Sprintf("%d.bin", i))
		part.Write([]byte{byte(i)})
	}
	form.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/lanes/"+itoa(lane)+"/jobs", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at most 20 files") {
		t.Fatalf("too many attachments: %d %s", w.Code, w.Body.String())
	}
}

func TestReplyAcceptsMultipartFiles(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"reply-files@example.com","password":"password1"}`)
	var lane, user int64
	a.DB.QueryRow("SELECT l.id,l.user_id FROM lanes l JOIN users u ON u.id=l.user_id WHERE u.email=?", "reply-files@example.com").Scan(&lane, &user)
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES(?,?,?,'done',0)", user, lane, "review")
	job, _ := res.LastInsertId()
	res, _ = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'finished','done')", job)
	run, _ := res.LastInsertId()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("comment", "Inspect this")
	part, _ := form.CreateFormFile("files", "sample.bin")
	part.Write([]byte{0xff, 0x00, 0x01})
	form.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/jobs/"+itoa(job)+"/comment", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	var content string
	if err = a.DB.QueryRow("SELECT content FROM job_events WHERE job_run_id=? AND kind='comment'", run).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Attached file: sample.bin\n```\nBase64-encoded content:\n/wAB") {
		t.Fatalf("reply content: %q", content)
	}
}

func req(t *testing.T, h http.Handler, c *http.Cookie, method, path, body string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == "session" {
			out = x
		}
	}
	return w, out
}
func TestV2WorkspaceOwnershipAliasesAndCustomTools(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := Open(filepath.Join(t.TempDir(), "db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@example.com","password":"password1"}`)
	_, other := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"other@example.com","password":"password2"}`)

	w, _ := req(t, h, owner, "GET", "/api/workspaces", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"owner"`) || strings.Contains(w.Body.String(), "projectDirectory") {
		t.Fatalf("workspaces: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/projects", `{"name":"App","directory":"project"}`)
	if w.Code != 201 {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	var projectOut map[string]any
	json.Unmarshal(w.Body.Bytes(), &projectOut)
	projectID := int64(projectOut["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/projects", `{"name":"Escape","directory":"../escape"}`)
	if w.Code != 400 {
		t.Fatalf("directory escape accepted: %d", w.Code)
	}
	w, _ = req(t, h, other, "GET", "/api/projects/"+itoa(projectID), "")
	if w.Code != 404 {
		t.Fatalf("cross-user project access=%d", w.Code)
	}
	w, _ = req(t, h, owner, "POST", "/api/cli-tools", `{"name":"Shell","argv":["sh","-c","printf ok"]}`)
	if w.Code != 201 {
		t.Fatalf("tool: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "GET", "/api/boards", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Default Board"`) || !strings.Contains(w.Body.String(), `"workspaceName":"Default"`) {
		t.Fatalf("signup default board: %d %s", w.Code, w.Body.String())
	}
}

func TestHermesSettingsAreWorkspaceScoped(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"hermes@example.com","password":"password1"}`)
	w, _ := req(t, h, cookie, "GET", "/api/workspaces", "")
	var workspaces []map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspaces)
	firstID := int64(workspaces[0]["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/workspaces", `{"name":"Second"}`)
	var second map[string]any
	json.Unmarshal(w.Body.Bytes(), &second)
	secondID := int64(second["id"].(float64))

	w, _ = req(t, h, cookie, "PATCH", "/api/workspaces/"+itoa(firstID)+"/settings", `{"hermes_url":"","hermes_api_key":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing config accepted: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "PATCH", "/api/workspaces/"+itoa(firstID)+"/settings", `{"hermes_url":"http://127.0.0.1:9999","hermes_api_key":"secret","hermes_model":"hermes-agent"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "default_cli") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/workspaces/"+itoa(secondID)+"/settings", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hermes_url":""`) || strings.Contains(w.Body.String(), `127.0.0.1`) {
		t.Fatalf("workspace settings leaked: %d %s", w.Code, w.Body.String())
	}
}

func TestRunHermesAcceptsV1BaseURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Hermes-Session-Id"); got != "session-123" {
			t.Errorf("session header=%q", got)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer ts.Close()
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.DB.Exec("INSERT INTO users(email,password_hash) VALUES('hermes-run@example.com','x')")
	a.DB.Exec("INSERT INTO user_settings(user_id,workspace_root,hermes_url,hermes_api_key,hermes_model) VALUES((SELECT id FROM users WHERE email='hermes-run@example.com'),?,?,?,?)", t.TempDir(), ts.URL+"/v1/", "secret", "hermes-agent")
	var userID int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='hermes-run@example.com'").Scan(&userID)
	res, _ := a.DB.Exec("INSERT INTO workspaces(user_id,name,root,hermes_url,hermes_api_key,hermes_model) VALUES(?,'Test','',?,?,?)", userID, ts.URL+"/v1/", "secret", "hermes-agent")
	workspaceID, _ := res.LastInsertId()
	got, err := a.runHermesSession(context.Background(), workspaceID, "Reply OK", "session-123")
	if err != nil || got != "OK" {
		t.Fatalf("runHermes: got %q, err %v", got, err)
	}
}

func TestRetryQueuesAtLaneEndThenSchedulerReusesLatestHermesSessionAndRun(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	var hermesCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/latest-session/messages" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.URL.Path == "/api/sessions/latest-session" {
			w.Write([]byte(`{"session":{"title":null}}`))
			return
		}
		if got := r.Header.Get("X-Hermes-Session-Id"); got != "latest-session" {
			t.Errorf("session header=%q, want latest-session", got)
		}
		hermesCalls.Add(1)
		var body struct {
			Messages []struct {
				Role, Content string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "retry" {
			t.Errorf("retry request body=%+v err=%v", body, err)
		}
		requestSeen <- struct{}{}
		w.Write([]byte(`{"choices":[{"message":{"content":"retried"}}]}`))
	}))
	defer ts.Close()

	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"retry@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='retry@example.com'").Scan(&user)
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
	a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Lane 1',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)
	a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user)
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position,attempt_count,finished_at) VALUES(?,?,'work','done',0,1,CURRENT_TIMESTAMP)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:older-session','done',CURRENT_TIMESTAMP)", job)
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:latest-session','done',CURRENT_TIMESTAMP)", job)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()
	if _, err = a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES(?,?,'already queued','todo',1)", user, lane); err != nil {
		t.Fatal(err)
	}

	w, _ := req(t, h, cookie, "POST", "/api/jobs/"+itoa(job)+"/retry", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("retry=%d %s", w.Code, w.Body.String())
	}
	if got := hermesCalls.Load(); got != 0 {
		t.Fatalf("Hermes calls immediately after retry=%d, want 0", got)
	}
	var state, pending string
	var position int
	if err = a.DB.QueryRow("SELECT state,pending_comment,position FROM jobs WHERE id=?", job).Scan(&state, &pending, &position); err != nil {
		t.Fatal(err)
	}
	if state != "todo" || pending != "retry" || position != 2 {
		t.Fatalf("queued retry state=%q pending=%q position=%d; want todo, retry, 2", state, pending, position)
	}
	var attempts, runs, runningRuns int
	a.DB.QueryRow("SELECT attempt_count FROM jobs WHERE id=?", job).Scan(&attempts)
	a.DB.QueryRow("SELECT count(*) FROM job_runs WHERE job_id=?", job).Scan(&runs)
	a.DB.QueryRow("SELECT count(*) FROM job_runs WHERE job_id=? AND status='running'", job).Scan(&runningRuns)
	if attempts != 1 || runs != 2 || runningRuns != 0 {
		t.Fatalf("attempts=%d runs=%d running=%d, want unchanged 1, 2, 0", attempts, runs, runningRuns)
	}
	var status, summary string
	a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE id=?", run).Scan(&status, &summary)
	if status != "done" || summary != "" {
		t.Fatalf("latest run status=%q summary=%q", status, summary)
	}
	var retryEvent string
	if err = a.DB.QueryRow("SELECT content FROM job_events WHERE job_run_id=? AND kind='retry'", run).Scan(&retryEvent); err != nil {
		t.Fatal(err)
	}
	if retryEvent != "Reply sent — pending" {
		t.Fatalf("retry timeline event=%q", retryEvent)
	}

	if _, err = a.DB.Exec("UPDATE jobs SET archived=1 WHERE lane_id=? AND id<>?", lane, job); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("UPDATE lanes SET paused=0 WHERE id=?", lane); err != nil {
		t.Fatal(err)
	}
	a.schedule()
	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Hermes retry request not received after scheduling")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&state)
		if state == "in_review" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%q, want in_review", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE id=?", run).Scan(&status, &summary)
	if status != "done" || summary != "retried" {
		t.Fatalf("scheduled run status=%q summary=%q", status, summary)
	}
}

func TestApprovalQueuesAtLaneEndThenSchedulerReusesLatestHermesSessionAndRun(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	var hermesCalls atomic.Int32
	var a *App
	var job, latestRun int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/latest-approval-session/messages" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.URL.Path == "/api/sessions/latest-approval-session" {
			w.Write([]byte(`{"session":{"title":null}}`))
			return
		}
		hermesCalls.Add(1)
		if got := r.Header.Get("X-Hermes-Session-Id"); got != "latest-approval-session" {
			t.Errorf("session header=%q, want latest-approval-session", got)
		}
		var state, phase, pending, runStatus string
		if err := a.DB.QueryRow(`SELECT j.state,j.phase,j.pending_comment,r.status FROM jobs j JOIN job_runs r ON r.id=? WHERE j.id=?`, latestRun, job).Scan(&state, &phase, &pending, &runStatus); err != nil {
			t.Errorf("read scheduled approval state: %v", err)
		} else if state != "in_progress" || phase != "implementation" || pending != "" || runStatus != "running" {
			t.Errorf("state at Hermes request=%q phase=%q pending=%q run=%q; want in_progress, implementation, empty, running", state, phase, pending, runStatus)
		}
		var body struct {
			Messages []struct {
				Role, Content string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "The proposal is explicitly approved. Implement it now, verify the work, and report the completed result.\n\nApproval reply:\nGo ahead and preserve the retry behavior." {
			t.Errorf("approval request body=%+v err=%v", body, err)
		}
		requestSeen <- struct{}{}
		w.Write([]byte(`{"choices":[{"message":{"content":"implemented"}}]}`))
	}))
	defer ts.Close()

	var err error
	a, err = Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"queued-approval@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='queued-approval@example.com'").Scan(&user)
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
	a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Approval lane',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lane)
	a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user)
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,phase,position,attempt_count) VALUES(?,?,'approved work','in_review','review',0,1)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ = res.LastInsertId()
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'hermes-api:older-approval-session','done',CURRENT_TIMESTAMP)", job)
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at,result_summary) VALUES(?,1,'hermes-api:latest-approval-session','done',CURRENT_TIMESTAMP,'proposal')", job)
	if err != nil {
		t.Fatal(err)
	}
	latestRun, _ = res.LastInsertId()
	res, err = a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES(?,?,'already queued','todo',1)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	queuedJob, _ := res.LastInsertId()

	w, _ := req(t, h, cookie, "POST", "/api/jobs/"+itoa(job)+"/comment", `{"comment":"Go ahead and preserve the retry behavior."}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approve by reply=%d %s", w.Code, w.Body.String())
	}
	if got := hermesCalls.Load(); got != 0 {
		t.Fatalf("Hermes calls immediately after approval=%d, want 0", got)
	}
	var state, phase, pending, runStatus, summary string
	var position int
	if err = a.DB.QueryRow("SELECT state,phase,pending_comment,position FROM jobs WHERE id=?", job).Scan(&state, &phase, &pending, &position); err != nil {
		t.Fatal(err)
	}
	if state != "todo" || phase != "implementation" || pending != implementationApprovalPendingPrefix+"Go ahead and preserve the retry behavior." || position != 2 {
		t.Fatalf("queued approval state=%q phase=%q pending=%q position=%d; want todo, implementation, approval reply, 2", state, phase, pending, position)
	}
	a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE id=?", latestRun).Scan(&runStatus, &summary)
	if runStatus != "done" || summary != "proposal" {
		t.Fatalf("latest run at approval status=%q summary=%q; want unchanged done/proposal", runStatus, summary)
	}

	w, _ = req(t, h, cookie, "POST", "/api/jobs/"+itoa(job)+"/approve", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent approve=%d %s", w.Code, w.Body.String())
	}
	var approvals, comments int
	a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='approval'", latestRun).Scan(&approvals)
	a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='comment'", latestRun).Scan(&comments)
	if approvals != 1 || comments != 1 {
		t.Fatalf("approval events after duplicate request: approvals=%d comments=%d; want 1 each", approvals, comments)
	}

	if _, err = a.DB.Exec("UPDATE jobs SET archived=1 WHERE id=?", queuedJob); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("UPDATE lanes SET paused=0 WHERE id=?", lane); err != nil {
		t.Fatal(err)
	}
	a.schedule()
	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Hermes implementation request not received after scheduling")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&state)
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%q, want done", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE id=?", latestRun).Scan(&runStatus, &summary)
	if runStatus != "done" || summary != "implemented" {
		t.Fatalf("scheduled implementation run status=%q summary=%q", runStatus, summary)
	}
}

func TestReconcileHermesRestartBlockFromCurrentSession(t *testing.T) {
	for _, tc := range []struct {
		name, messages, wantState, wantRun string
	}{
		{"completed", `{"object":"list","data":[{"role":"user","content":"work"},{"id":"progress-1","role":"assistant","content":"Inspecting the repository"},{"id":"progress-2","role":"assistant","content":"Running focused tests"},{"id":"final-1","role":"assistant","content":"finished remotely"}]}`, "in_review", "done"},
		{"active", `{"object":"list","data":[{"role":"user","content":"work"}]}`, "in_progress", "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("authorization=%q", r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/api/sessions/hermes-session":
					w.Write([]byte(`{"object":"hermes.session","session":{"id":"hermes-session","ended_at":null,"end_reason":null}}`))
				case "/api/sessions/hermes-session/messages":
					w.Write([]byte(tc.messages))
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"recover@example.com","password":"password1"}`)
			var user, lane int64
			a.DB.QueryRow("SELECT id FROM users WHERE email='recover@example.com'").Scan(&user)
			a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
			a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Lane 1',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user)
			a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL+"/v1", user)
			res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,warning,position,attempt_count) VALUES(?,?,'work','blocked','Execution session missing after server restart',0,1)", user, lane)
			if err != nil {
				t.Fatal(err)
			}
			job, _ := res.LastInsertId()
			res, _ = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,result_summary,ended_at) VALUES(?,1,'hermes-api:hermes-session','blocked','Execution session missing after server restart',CURRENT_TIMESTAMP)", job)
			run, _ := res.LastInsertId()
			a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'error','Execution session missing after server restart')", run)

			a.reconcile()
			a.reconcileHermes(job, run, "hermes-session")

			var state, warning, runStatus string
			a.DB.QueryRow("SELECT state,warning FROM jobs WHERE id=?", job).Scan(&state, &warning)
			a.DB.QueryRow("SELECT status FROM job_runs WHERE id=?", run).Scan(&runStatus)
			if state != tc.wantState || runStatus != tc.wantRun || warning != "" {
				t.Fatalf("state=%q run=%q warning=%q", state, runStatus, warning)
			}
			var original int
			a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='error'", run).Scan(&original)
			if original != 1 {
				t.Fatalf("original timeline events=%d", original)
			}
			if tc.wantState == "in_review" {
				var output int
				a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=? AND kind='reply' AND content='finished remotely'", run).Scan(&output)
				if output != 1 {
					t.Fatalf("recovered output events=%d", output)
				}
				rows, queryErr := a.DB.Query("SELECT content FROM job_events WHERE job_run_id=? AND kind='intermediary' ORDER BY sequence", run)
				if queryErr != nil {
					t.Fatal(queryErr)
				}
				var intermediaries []string
				for rows.Next() {
					var content string
					if queryErr = rows.Scan(&content); queryErr != nil {
						rows.Close()
						t.Fatal(queryErr)
					}
					intermediaries = append(intermediaries, content)
				}
				rows.Close()
				if strings.Join(intermediaries, "|") != "Inspecting the repository|Running focused tests" {
					t.Fatalf("recovered intermediaries=%v", intermediaries)
				}
			}
		})
	}
}

func TestInitialHermesPromptIncludesColumnProject(t *testing.T) {
	prompt := initialHermesPrompt("Paragentix", "/srv/projects/paragentix", "Fix the scheduler", "Tests pass")
	want := "Unless otherwise specified, this conversation concerns the project Paragentix, located at /srv/projects/paragentix. Use this project as the default when creating or modifying jobs. Use the direct terminal tool with /srv/projects/paragentix as the workdir for shell commands; do not wrap terminal in execute_code. Delegated shell work must request terminal explicitly. If an indirect terminal attempt fails, retry with the direct terminal tool before claiming terminal is unavailable.\n\nDo not implement the requested work yet. Analyze it, inspect the project as needed, and return a concrete proposal for explicit review and approval.\n\nFix the scheduler\n\nDone definition:\nTests pass"
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
}

func TestSettingsRequireAuthentication(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w, _ := req(t, a.Handler(), nil, "GET", "/api/workspaces/1/settings", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out settings access=%d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestNotificationsArePaginatedAndOwnerScoped(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"notify@example.com","password":"password1"}`)
	req(t, h, nil, "POST", "/api/auth/signup", `{"email":"other-notify@example.com","password":"password1"}`)
	var ownerID, otherID int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='notify@example.com'").Scan(&ownerID)
	a.DB.QueryRow("SELECT id FROM users WHERE email='other-notify@example.com'").Scan(&otherID)
	for i := 0; i < 12; i++ {
		a.DB.Exec("INSERT INTO notifications(user_id,job_id,kind,title) VALUES(?,NULL,'done',?)", ownerID, fmt.Sprintf("Job %02d", i))
	}
	a.DB.Exec("INSERT INTO notifications(user_id,job_id,kind,title) VALUES(?,NULL,'error','Private')", otherID)
	w, _ := req(t, h, owner, "GET", "/api/notifications?limit=10", "")
	var page struct {
		Notifications []map[string]any `json:"notifications"`
		HasMore       bool             `json:"has_more"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || w.Code != 200 || len(page.Notifications) != 10 || !page.HasMore || strings.Contains(w.Body.String(), "Private") {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	first := int64(page.Notifications[0]["id"].(float64))
	w, _ = req(t, h, owner, "PATCH", "/api/notifications/"+itoa(first), `{"read":true}`)
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/notifications/mark-read", `{}`)
	if w.Code != 200 {
		t.Fatalf("mark read: %d %s", w.Code, w.Body.String())
	}
	var unread int
	a.DB.QueryRow("SELECT count(*) FROM notifications WHERE user_id=? AND read=0", ownerID).Scan(&unread)
	if unread != 0 {
		t.Fatalf("owner has %d unread notifications after marking all read", unread)
	}
	a.DB.QueryRow("SELECT count(*) FROM notifications WHERE user_id=? AND read=0", otherID).Scan(&unread)
	if unread != 1 {
		t.Fatalf("other user has %d unread notifications, want 1", unread)
	}
}

func TestJobCompletionNotifiesOnlyCreator(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, creator := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"creator-notify@example.com","password":"password1"}`)
	_, other := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"other-completion@example.com","password":"password1"}`)

	var creatorID, laneID int64
	if err = a.DB.QueryRow(`SELECT u.id,l.id FROM users u JOIN lanes l ON l.user_id=u.id WHERE u.email=?`, "creator-notify@example.com").Scan(&creatorID, &laneID); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,position) VALUES(?,?,'private work','done',0)", creatorID, laneID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	res, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'finished','done')", jobID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()

	a.notify(jobID, runID, "done")

	for name, tc := range map[string]struct {
		cookie *http.Cookie
		want   int
	}{
		"creator":    {creator, 1},
		"other user": {other, 0},
	} {
		w, _ := req(t, h, tc.cookie, "GET", "/api/notifications", "")
		var page struct {
			Notifications []map[string]any `json:"notifications"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s notifications: %d %s", name, w.Code, w.Body.String())
		}
		if got := len(page.Notifications); got != tc.want {
			t.Fatalf("%s received %d completion notifications, want %d", name, got, tc.want)
		}
	}
}

func TestObsoleteColumnMigration(t *testing.T) {
	var err error
	db := filepath.Join(t.TempDir(), "existing.db")
	a, err := Open(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO users(email,password_hash) VALUES('legacy@example.com',x'00')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO user_settings(user_id,workspace_root) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO lanes(user_id,name,position,paused) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'legacy lane',0,1);
INSERT INTO jobs(user_id,lane_id,task,position,pending_comment) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),(SELECT id FROM lanes WHERE name='legacy lane'),'preserve me',0,'follow up')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`ALTER TABLE user_settings ADD COLUMN default_cli TEXT NOT NULL DEFAULT 'codex';
ALTER TABLE jobs ADD COLUMN cli_tool TEXT NOT NULL DEFAULT 'codex';
ALTER TABLE custom_cli_tools ADD COLUMN command TEXT NOT NULL DEFAULT '';
INSERT INTO custom_cli_tools(user_id,name,command,argv_json) VALUES((SELECT id FROM users WHERE email='legacy@example.com'),'local-agent','local-agent --safe','["local-agent","--safe"]');`); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = Open(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var n int
	if err = a.DB.QueryRow(`SELECT count(*) FROM users WHERE email='legacy@example.com'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy data lost: %d %v", n, err)
	}
	for _, table := range []string{"workspaces", "boards", "columns", "custom_cli_tools"} {
		if err = a.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('custom_cli_tools') WHERE name='command'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("obsolete command column remains: %d %v", n, err)
	}
	for table, column := range map[string]string{"user_settings": "default_cli", "jobs": "cli_tool"} {
		if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n); err != nil || n != 0 {
			t.Fatalf("obsolete %s.%s remains: %d %v", table, column, n, err)
		}
	}
	var argv string
	if err = a.DB.QueryRow(`SELECT argv_json FROM custom_cli_tools WHERE name='local-agent'`).Scan(&argv); err != nil || argv != `["local-agent","--safe"]` {
		t.Fatalf("custom tool lost: %q %v", argv, err)
	}
	var task, comment string
	if err = a.DB.QueryRow(`SELECT task,pending_comment FROM jobs WHERE task='preserve me'`).Scan(&task, &comment); err != nil || task != "preserve me" || comment != "follow up" {
		t.Fatalf("job data lost: task=%q comment=%q err=%v", task, comment, err)
	}
	if err = a.migrate(); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
}

func TestFreshAccountLanesReturnEmptyJobsArray(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	w, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"fresh@example.com","password":"password1"}`)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	w, _ = req(t, h, cookie, "GET", "/api/lanes", "")
	if !strings.Contains(w.Body.String(), `"jobs":[]`) {
		t.Fatalf("empty jobs must be an array: %s", w.Body.String())
	}
}

func TestV2ColumnUsesMappedLane(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "new")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := Open(filepath.Join(t.TempDir(), "db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"column@example.com","password":"password1"}`)
	w, _ := req(t, h, cookie, "POST", "/api/workspaces", `{"name":"New","projectDirectory":"`+workspace+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	workspaceID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/boards", `{"name":"Board","workspaceId":`+itoa(workspaceID)+`}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	boardID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/workspaces/"+itoa(workspaceID)+"/projects", `{"name":"App","directory":"`+workspace+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	projectID := int64(out["id"].(float64))
	w, _ = req(t, h, cookie, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("column: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	columnID := int64(out["id"].(float64))
	var laneID int64
	if err := a.DB.QueryRow(`SELECT lane_id FROM columns WHERE id=?`, columnID).Scan(&laneID); err != nil || laneID == columnID {
		t.Fatalf("column/lane mapping: column=%d lane=%d err=%v", columnID, laneID, err)
	}
	w, _ = req(t, h, cookie, "POST", "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"mapped","done_definition":"done"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("job: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := a.DB.QueryRow(`SELECT count(*) FROM jobs WHERE lane_id=?`, laneID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("job lane: count=%d err=%v", n, err)
	}
	w, _ = req(t, h, cookie, "GET", "/api/boards/"+itoa(boardID)+"/columns", "")
	var columns []struct {
		Jobs []Job `json:"jobs"`
	}
	if json.Unmarshal(w.Body.Bytes(), &columns) != nil || len(columns) != 1 || len(columns[0].Jobs) != 1 || columns[0].Jobs[0].Task != "mapped" || columns[0].Jobs[0].Creator != "column@example.com" {
		t.Fatalf("column jobs: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthIsolationAndStateValidation(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	w, c1 := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"a@example.com","password":"password1"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, c2 := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"b@example.com","password":"password2"}`)
	w, _ = req(t, h, c1, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	// Keep the scheduler from racing this API state-validation test.
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, h, c1, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello","done_definition":"works"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	w, _ = req(t, h, c2, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user access=%d", w.Code)
	}
	w, _ = req(t, h, c1, "PATCH", "/api/jobs/"+itoa(id), `{"done_definition":"eligible change"}`)
	var doneDefinition string
	if e := a.DB.QueryRow("SELECT done_definition FROM jobs WHERE id=?", id).Scan(&doneDefinition); w.Code != 200 || e != nil || doneDefinition != "eligible change" {
		t.Fatalf("unexecuted todo edit=%d definition=%q err=%v", w.Code, doneDefinition, e)
	}
	a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP WHERE id=?", id)
	w, _ = req(t, h, c1, "PATCH", "/api/jobs/"+itoa(id), `{"done_definition":"changed"}`)
	if w.Code != 409 {
		t.Fatalf("done edit=%d", w.Code)
	}
	a.DB.Exec("UPDATE jobs SET state='todo',attempt_count=1,finished_at=NULL WHERE id=?", id)
	w, _ = req(t, h, c1, "PATCH", "/api/jobs/"+itoa(id), `{"done_definition":"changed"}`)
	if w.Code != 409 {
		t.Fatalf("executed todo edit=%d", w.Code)
	}
	a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP WHERE id=?", id)
	w, _ = req(t, h, c1, "POST", "/api/jobs/"+itoa(id)+"/retry", `{}`)
	if w.Code != 409 {
		t.Fatalf("retry without Hermes session=%d %s", w.Code, w.Body.String())
	}
	var state string
	var finished any
	if e := a.DB.QueryRow("SELECT state,finished_at FROM jobs WHERE id=?", id).Scan(&state, &finished); e != nil || state != "done" || finished == nil {
		t.Fatalf("rejected retry changed job state=%q finished=%v err=%v", state, finished, e)
	}
	var retryEvents int
	a.DB.QueryRow(`SELECT count(*) FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? AND e.kind='retry'`, id).Scan(&retryEvents)
	if retryEvents != 0 {
		t.Fatalf("rejected retry timeline events=%d", retryEvents)
	}
	w, _ = req(t, h, c2, "DELETE", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user archive=%d", w.Code)
	}
	res, e := a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'archived-test','done')", id)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	if _, e = a.DB.Exec("INSERT INTO job_events(job_run_id,sequence,kind,content) VALUES(?,1,'output','done')", runID); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, c1, "DELETE", "/api/jobs/"+itoa(id), "")
	if w.Code != 204 {
		t.Fatalf("archive done=%d %s", w.Code, w.Body.String())
	}
	var count int
	a.DB.QueryRow("SELECT COUNT(*) FROM jobs WHERE id=? AND archived=1", id).Scan(&count)
	if count != 1 {
		t.Fatal("archived job was not retained")
	}
	a.DB.QueryRow("SELECT COUNT(*) FROM job_runs WHERE id=?", runID).Scan(&count)
	if count != 1 {
		t.Fatal("archived job run was not retained")
	}
	w, _ = req(t, h, c1, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"archive"`) || !strings.Contains(w.Body.String(), `"content":"done"`) || !strings.Contains(w.Body.String(), `"session_id":"archived-test"`) {
		t.Fatalf("archived detail history: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, c2, "GET", "/api/jobs/"+itoa(id), "")
	if w.Code != 404 {
		t.Fatalf("cross-user archived detail=%d", w.Code)
	}
}
func TestJobCommentSendsToActiveSessionAndRecordsEvent(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	_, c := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"comment@example.com","password":"password1"}`)
	w, _ := req(t, h, c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, h, c, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello","done_definition":"works"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	a.DB.Exec("UPDATE jobs SET state='in_progress' WHERE id=?", id)
	r, _ := a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,?,'running')", id, "agent-job-"+itoa(id))
	runID, _ := r.LastInsertId()
	session := "agent-job-" + itoa(id)
	if e := exec.Command("tmux", "new-session", "-d", "-s", session).Run(); e != nil {
		t.Skip("tmux unavailable:", e)
	}
	defer exec.Command("tmux", "kill-session", "-t", session).Run()

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"keep the API shape"}`)
	if w.Code != 200 {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	var kind, content string
	if e := a.DB.QueryRow("SELECT kind,content FROM job_events WHERE job_run_id=?", runID).Scan(&kind, &content); e != nil || kind != "comment" || content != "keep the API shape" {
		t.Fatalf("event: kind=%q content=%q err=%v", kind, content, e)
	}

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"   "}`)
	if w.Code != 400 {
		t.Fatalf("blank comment=%d", w.Code)
	}
	exec.Command("tmux", "kill-session", "-t", session).Run()
	a.DB.Exec("UPDATE jobs SET state='done' WHERE id=?", id)
	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(id)+"/comment", `{"comment":"late"}`)
	if w.Code != 200 {
		t.Fatalf("done comment=%d", w.Code)
	}
	var state string
	a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", id).Scan(&state)
	if state != "todo" && state != "in_progress" {
		t.Fatalf("done comment state=%q", state)
	}
}

func TestCommentOnDoneJobRequeuesAtEndOfTodoOrder(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	h := a.Handler()
	_, c := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"requeue-comment@example.com","password":"password1"}`)
	w, _ := req(t, h, c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	laneID := lanes[0].ID
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", laneID)

	ids := make([]int64, 3)
	for i, task := range []string{"completed", "todo one", "todo two"} {
		w, _ = req(t, h, c, "POST", "/api/lanes/"+itoa(laneID)+"/jobs", `{"task":"`+task+`","done_definition":"works"}`)
		var made map[string]any
		json.Unmarshal(w.Body.Bytes(), &made)
		ids[i] = int64(made["id"].(float64))
	}
	a.DB.Exec("UPDATE jobs SET state='done',finished_at=CURRENT_TIMESTAMP WHERE id=?", ids[0])
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status,ended_at) VALUES(?,1,'completed-run','done',CURRENT_TIMESTAMP)", ids[0])

	w, _ = req(t, h, c, "POST", "/api/jobs/"+itoa(ids[0])+"/comment", `{"comment":"follow up"}`)
	if w.Code != 200 {
		t.Fatalf("done comment: %d %s", w.Code, w.Body.String())
	}
	var state string
	var position int
	if e = a.DB.QueryRow("SELECT state,position FROM jobs WHERE id=?", ids[0]).Scan(&state, &position); e != nil || state != "todo" || position != 3 {
		t.Fatalf("requeued job: state=%q position=%d err=%v", state, position, e)
	}

	w, _ = req(t, h, c, "GET", "/api/lanes", "")
	json.Unmarshal(w.Body.Bytes(), &lanes)
	var todoIDs []int64
	for _, job := range lanes[0].Jobs {
		if job.State == "todo" {
			todoIDs = append(todoIDs, job.ID)
		}
	}
	if len(todoIDs) != 3 || todoIDs[0] != ids[1] || todoIDs[1] != ids[2] || todoIDs[2] != ids[0] {
		t.Fatalf("todo display order=%v, want [%d %d %d]", todoIDs, ids[1], ids[2], ids[0])
	}
}

func TestReconcileKeepsWatchingHermesRunAfterTransientAPIFailure(t *testing.T) {
	var sessionRequests atomic.Int32
	var a *App
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/live-session":
			if sessionRequests.Add(1) == 1 {
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(`{"session":{"id":"live-session","ended_at":null,"end_reason":null}}`))
		case "/api/sessions/live-session/messages":
			w.Write([]byte(`{"data":[{"role":"assistant","content":"Implemented after restart"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var err error
	a, err = Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"reconcile-hermes@example.com","password":"password1"}`)
	var user, lane int64
	a.DB.QueryRow("SELECT id FROM users WHERE email='reconcile-hermes@example.com'").Scan(&user)
	a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane)
	a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Lane 1',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user)
	a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user)
	res, _ := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,phase,position,attempt_count) VALUES(?,?,'work','in_progress','implementation',0,1)", user, lane)
	job, _ := res.LastInsertId()
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'hermes-api:live-session','running')", job)

	a.reconcile()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job).Scan(&state)
		if state == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job was abandoned after transient API failure; session requests=%d", sessionRequests.Load())
}

func TestOpenReconcilesCompletedImplementationHermesRunAfterTransientFailure(t *testing.T) {
	var sessionRequests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/restarted-session":
			if sessionRequests.Add(1) == 1 {
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(`{"session":{"id":"restarted-session","ended_at":"2026-07-30T14:00:00Z","end_reason":"completed"}}`))
		case "/api/sessions/restarted-session/messages":
			w.Write([]byte(`{"data":[{"id":87591,"role":"assistant","content":"Implemented and pushed commit 93e0e9b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	dbPath := filepath.Join(t.TempDir(), "db")
	a, err := Open(dbPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"startup-reconcile@example.com","password":"password1"}`)
	var user, lane int64
	if err = a.DB.QueryRow("SELECT id FROM users WHERE email='startup-reconcile@example.com'").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow("SELECT id FROM lanes WHERE user_id=?", user).Scan(&lane); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) SELECT ?,b.id,?,p.id,'Lane 1',0 FROM boards b JOIN projects p ON p.workspace_id=b.workspace_id WHERE b.user_id=? LIMIT 1", user, lane, user); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec("UPDATE workspaces SET hermes_url=?,hermes_api_key='secret' WHERE user_id=?", ts.URL, user); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec("INSERT INTO jobs(user_id,lane_id,task,state,phase,position,attempt_count) VALUES(?,?,'work','in_progress','implementation',0,1)", user, lane)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := res.LastInsertId()
	if _, err = a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,'hermes-api:restarted-session','running')", job); err != nil {
		t.Fatal(err)
	}
	a.Close()

	a, err = Open(dbPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state, runStatus, summary string
		var attempts, runs, replies int
		if err = a.DB.QueryRow("SELECT state,attempt_count FROM jobs WHERE id=?", job).Scan(&state, &attempts); err != nil {
			t.Fatal(err)
		}
		if err = a.DB.QueryRow("SELECT status,result_summary FROM job_runs WHERE job_id=?", job).Scan(&runStatus, &summary); err != nil {
			t.Fatal(err)
		}
		if err = a.DB.QueryRow("SELECT count(*) FROM job_runs WHERE job_id=?", job).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if err = a.DB.QueryRow("SELECT count(*) FROM job_events WHERE job_run_id=(SELECT id FROM job_runs WHERE job_id=?) AND kind='reply'", job).Scan(&replies); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			if runStatus != "done" || summary != "Implemented and pushed commit 93e0e9b" || attempts != 1 || runs != 1 || replies != 1 {
				t.Fatalf("state=%q run=%q summary=%q attempts=%d runs=%d replies=%d", state, runStatus, summary, attempts, runs, replies)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("startup reconciliation did not finish completed Hermes run; session requests=%d", sessionRequests.Load())
}

func TestReconcileBlocksRunningRunWithoutTmuxSession(t *testing.T) {
	a, e := Open(t.TempDir()+"/db", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	_, c := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"reconcile@example.com","password":"password1"}`)
	w, _ := req(t, a.Handler(), c, "GET", "/api/lanes", "")
	var lanes []Lane
	json.Unmarshal(w.Body.Bytes(), &lanes)
	a.DB.Exec("UPDATE lanes SET paused=1 WHERE id=?", lanes[0].ID)
	w, _ = req(t, a.Handler(), c, "POST", "/api/lanes/"+itoa(lanes[0].ID)+"/jobs", `{"task":"hello"}`)
	var made map[string]any
	json.Unmarshal(w.Body.Bytes(), &made)
	id := int64(made["id"].(float64))
	a.DB.Exec("UPDATE jobs SET state='in_progress' WHERE id=?", id)
	a.DB.Exec("INSERT INTO job_runs(job_id,attempt,tmux_session,status) VALUES(?,1,?,'running')", id, "missing-session")

	a.reconcile()

	var runStatus, warning string
	a.DB.QueryRow("SELECT status FROM job_runs WHERE job_id=?", id).Scan(&runStatus)
	a.DB.QueryRow("SELECT warning FROM jobs WHERE id=?", id).Scan(&warning)
	if runStatus != "blocked" || warning != "Execution session missing after server restart" {
		t.Fatalf("status=%q warning=%q", runStatus, warning)
	}
	var statusEvents int
	a.DB.QueryRow(`SELECT count(*) FROM job_events e JOIN job_runs r ON r.id=e.job_run_id WHERE r.job_id=? AND e.kind='status' AND e.content LIKE '%in_progress%blocked%'`, id).Scan(&statusEvents)
	if statusEvents != 1 {
		t.Fatalf("blocked status timeline events=%d", statusEvents)
	}
}

func itoa(n int64) string {
	const d = "0123456789"
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for ; n > 0; n /= 10 {
		b = append([]byte{d[n%10]}, b...)
	}
	return string(b)
}

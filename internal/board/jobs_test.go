package board

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateJobScheduleJSONAndMultipart(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	h := a.Handler()
	_, cookie := req(t, h, nil, http.MethodPost, "/api/auth/signup", `{"email":"scheduled-create@example.com","password":"password1"}`)
	var userID, boardID, projectID, laneID int64
	if err = a.DB.QueryRow(`SELECT u.id,b.id,p.id,l.id FROM users u JOIN boards b ON b.user_id=u.id JOIN projects p ON p.workspace_id=b.workspace_id JOIN lanes l ON l.user_id=u.id WHERE u.email=?`, "scheduled-create@example.com").Scan(&userID, &boardID, &projectID, &laneID); err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) VALUES(?,?,?,?,'Scheduled',0)`, userID, boardID, laneID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	columnID, _ := res.LastInsertId()
	a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, laneID)

	w, _ := req(t, h, cookie, http.MethodPost, "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"json scheduled","scheduledAt":"2099-10-03T12:30:00-04:00"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("JSON create: %d %s", w.Code, w.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("task", "multipart scheduled")
	writer.WriteField("columnId", itoa(columnID))
	writer.WriteField("scheduledAt", "2099-10-03T16:30:00Z")
	writer.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/boards/"+itoa(boardID)+"/jobs", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("multipart create: %d %s", w.Code, w.Body.String())
	}

	rows, err := a.DB.Query(`SELECT scheduled_at FROM jobs WHERE task IN('json scheduled','multipart scheduled') ORDER BY task`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var scheduled string
		if err = rows.Scan(&scheduled); err != nil || scheduled != "2099-10-03 16:30:00" {
			t.Fatalf("scheduled_at=%q err=%v", scheduled, err)
		}
	}
	if count != 2 {
		t.Fatalf("scheduled rows=%d, want 2", count)
	}
	var jobID int64
	a.DB.QueryRow(`SELECT id FROM jobs WHERE task='json scheduled'`).Scan(&jobID)
	for _, path := range []string{"/api/lanes", "/api/boards/" + itoa(boardID) + "/columns", "/api/jobs/" + itoa(jobID)} {
		w, _ = req(t, h, cookie, http.MethodGet, path, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"scheduled_at":"2099-10-03 16:30:00"`) {
			t.Fatalf("%s schedule response: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestCreateJobRejectsMalformedSchedulesWithoutCreatingJobs(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	h := a.Handler()
	_, cookie := req(t, h, nil, http.MethodPost, "/api/auth/signup", `{"email":"invalid-schedule@example.com","password":"password1"}`)
	var userID, boardID, projectID, laneID int64
	a.DB.QueryRow(`SELECT u.id,b.id,p.id,l.id FROM users u JOIN boards b ON b.user_id=u.id JOIN projects p ON p.workspace_id=b.workspace_id JOIN lanes l ON l.user_id=u.id WHERE u.email=?`, "invalid-schedule@example.com").Scan(&userID, &boardID, &projectID, &laneID)
	res, err := a.DB.Exec(`INSERT INTO columns(user_id,board_id,lane_id,project_id,name,position) VALUES(?,?,?,?,'Scheduled',0)`, userID, boardID, laneID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	columnID, _ := res.LastInsertId()

	w, _ := req(t, h, cookie, http.MethodPost, "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"bad json schedule","scheduledAt":"tomorrow"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("JSON status=%d body=%s", w.Code, w.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("task", "bad multipart schedule")
	writer.WriteField("columnId", itoa(columnID))
	writer.WriteField("scheduledAt", "not-a-time")
	writer.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/boards/"+itoa(boardID)+"/jobs", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("multipart status=%d body=%s", w.Code, w.Body.String())
	}

	var count int
	if err = a.DB.QueryRow(`SELECT count(*) FROM jobs WHERE task LIKE 'bad % schedule'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid schedules created %d jobs: %v", count, err)
	}
}

func TestJobDetailReturnsEmptyEventsArrayForTodoJob(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	h := a.Handler()
	_, cookie := req(t, h, nil, http.MethodPost, "/api/auth/signup", `{"email":"empty-events@example.com","password":"password1"}`)
	var userID, laneID int64
	if err = a.DB.QueryRow(`
		SELECT u.id,l.id FROM users u JOIN lanes l ON l.user_id=u.id
		WHERE u.email=?`, "empty-events@example.com").Scan(&userID, &laneID); err != nil {
		t.Fatal(err)
	}
	result, err := a.DB.Exec(`
		INSERT INTO jobs(user_id,lane_id,task,state,position)
		VALUES(?,?,'queued work','todo',0)`, userID, laneID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	response, _ := req(t, h, cookie, http.MethodGet, "/api/jobs/"+itoa(jobID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("job detail: %d %s", response.Code, response.Body.String())
	}
	var detail struct {
		Events json.RawMessage `json:"events"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if string(detail.Events) != "[]" {
		t.Fatalf("events=%s, want []", detail.Events)
	}
}

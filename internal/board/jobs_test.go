package board

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

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

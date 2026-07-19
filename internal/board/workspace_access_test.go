package board

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFrontendIncludesWorkspaceUserStatuses(t *testing.T) {
	index, err := fs.ReadFile(web, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`assets/(index-[^"']+\.js)`).FindSubmatch(index)
	if len(match) != 2 {
		t.Fatalf("embedded frontend index has no JavaScript asset: %s", index)
	}
	asset, err := fs.ReadFile(web, "web/assets/"+string(match[1]))
	if err != nil {
		t.Fatalf("embedded frontend asset %q: %v", match[1], err)
	}
	for _, label := range []string{"Invited", "Member"} {
		if !strings.Contains(string(asset), label) {
			t.Fatalf("embedded frontend asset %q does not include workspace user status %q", match[1], label)
		}
	}
}

func TestEmbeddedFrontendIncludesCompactReplyComposer(t *testing.T) {
	index, err := fs.ReadFile(web, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`assets/(index-[^"']+\.js)`).FindSubmatch(index)
	if match == nil {
		t.Fatalf("embedded frontend index has no JavaScript asset: %s", index)
	}
	asset, err := fs.ReadFile(web, "web/assets/"+string(match[1]))
	if err != nil {
		t.Fatalf("embedded frontend asset %q: %v", match[1], err)
	}
	for _, marker := range []string{"Reply to session", "Add files", "Send reply"} {
		if !bytes.Contains(asset, []byte(marker)) {
			t.Fatalf("embedded frontend asset %q does not include reply composer marker %q", match[1], marker)
		}
	}
	for _, legacy := range []string{"Type a comment or instruction", "Send comment"} {
		if bytes.Contains(asset, []byte(legacy)) {
			t.Fatalf("embedded frontend asset %q still includes legacy reply composer text %q", match[1], legacy)
		}
	}
}

type testMailer struct{ body *string }

func (m testMailer) Send(_, _, body string) error {
	if m.body != nil {
		*m.body = body
	}
	return nil
}

func TestResendMailer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request method=%q auth=%q content-type=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
		}
		var payload struct {
			From, Subject, Text string
			To                  []string
		}
		if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
			t.Fatal(e)
		}
		if payload.From != "Paragentix <invites@example.test>" || len(payload.To) != 1 || payload.To[0] != "user@example.test" || payload.Subject != "Workspace invitation" || payload.Text != "https://example.test/?invite=token" {
			t.Fatalf("payload=%+v", payload)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	m := ResendMailer{APIKey: "test-key", From: "Paragentix <invites@example.test>", URL: server.URL, Client: server.Client()}
	if e := m.Send("user@example.test", "Workspace invitation", "https://example.test/?invite=token"); e != nil {
		t.Fatal(e)
	}
}

func TestInvitationURLUsesRequestOriginByDefault(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var body string
	a.Mailer = testMailer{&body}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)

	r := httptest.NewRequest("POST", "http://app.example.test/api/workspaces/"+itoa(int64(workspace["id"].(float64)))+"/invitations", strings.NewReader(`{"email":"new@x.test"}`))
	r.Header.Set("X-Forwarded-Proto", "https")
	if r.TLS != nil {
		t.Fatal("reverse-proxy request unexpectedly has TLS state")
	}
	r.AddCookie(owner)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || !strings.HasPrefix(body, "https://app.example.test/?invite=") {
		t.Fatalf("invite=%d url=%q", w.Code, body)
	}
}

func TestInvitationURLAndReinviteAfterExpiry(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var body string
	a.Mailer, a.BaseURL = testMailer{&body}, "https://example.test/paragentix/"
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner2@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)
	wid := int64(workspace["id"].(float64))
	path := "/api/workspaces/" + itoa(wid) + "/invitations"
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 201 || !strings.HasPrefix(body, "https://example.test/paragentix/?invite=") {
		t.Fatalf("invite=%d url=%q", w.Code, body)
	}
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 409 {
		t.Fatalf("active duplicate=%d", w.Code)
	}
	if _, e = a.DB.Exec(`UPDATE workspace_invitations SET expires_at=datetime('now','-1 second') WHERE workspace_id=?`, wid); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, owner, "POST", path, `{"email":"new@x.test"}`)
	if w.Code != 201 {
		t.Fatalf("reinvite=%d %s", w.Code, w.Body.String())
	}
	var n int
	if e = a.DB.QueryRow(`SELECT count(*) FROM workspace_invitations WHERE workspace_id=? AND email=? AND accepted_at IS NULL`, wid, "new@x.test").Scan(&n); e != nil || n != 1 {
		t.Fatalf("active rows=%d err=%v", n, e)
	}
}

func TestInviteRejectsAuthenticatedUsersOwnEmail(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)

	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(int64(workspace["id"].(float64)))+"/invitations", `{"email":" Owner@X.Test "}`)
	if w.Code != http.StatusConflict || w.Body.String() != "{\"error\":\"cannot invite yourself\"}\n" {
		t.Fatalf("invite self=%d %s", w.Code, w.Body.String())
	}
}

func TestExistingUserInvitationCreatesNotificationAndSupportsAuthorizedModalState(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner-notify@x.test","password":"password1"}`)
	_, invited := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"invited@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Notify Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(int64(workspace["id"].(float64)))+"/invitations", `{"email":"invited@x.test"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite=%d %s", w.Code, w.Body.String())
	}
	var invitation map[string]any
	json.Unmarshal(w.Body.Bytes(), &invitation)
	id := int64(invitation["invitationId"].(float64))

	w, _ = req(t, h, invited, "GET", "/api/notifications", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"invitation"`) || !strings.Contains(w.Body.String(), `"invitation_id":`+itoa(id)) || !strings.Contains(w.Body.String(), "Notify Team") {
		t.Fatalf("notifications=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, invited, "GET", "/api/invitations/id/"+itoa(id), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"pending"`) {
		t.Fatalf("pending=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "GET", "/api/invitations/id/"+itoa(id), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-account preview=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, invited, "POST", "/api/invitations/id/"+itoa(id), "{}")
	if w.Code != 200 {
		t.Fatalf("accept=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, invited, "GET", "/api/invitations/id/"+itoa(id), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"accepted"`) {
		t.Fatalf("accepted=%d %s", w.Code, w.Body.String())
	}
}

func TestSignupDiscoversPendingInvitationOnlyOnceAndIgnoresExpired(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner-first@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"First Login Team"}`)
	var workspace map[string]any
	json.Unmarshal(w.Body.Bytes(), &workspace)
	path := "/api/workspaces/" + itoa(int64(workspace["id"].(float64))) + "/invitations"
	w, _ = req(t, h, owner, "POST", path, `{"email":"new-first@x.test"}`)
	var active map[string]any
	json.Unmarshal(w.Body.Bytes(), &active)
	w, _ = req(t, h, owner, "POST", path, `{"email":"expired-first@x.test"}`)
	var expired map[string]any
	json.Unmarshal(w.Body.Bytes(), &expired)
	a.DB.Exec(`UPDATE workspace_invitations SET expires_at=datetime('now','-1 second') WHERE id=?`, int64(expired["invitationId"].(float64)))

	_, fresh := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"new-first@x.test","password":"password1"}`)
	w, _ = req(t, h, fresh, "GET", "/api/invitations/active", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":`+itoa(int64(active["invitationId"].(float64)))) {
		t.Fatalf("first active=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, fresh, "GET", "/api/notifications", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"invitation"`) || !strings.Contains(w.Body.String(), "First Login Team") {
		t.Fatalf("new user notifications=%d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, fresh, "GET", "/api/invitations/active", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("second active=%d %s", w.Code, w.Body.String())
	}
	w, expiredUser := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"expired-first@x.test","password":"password1"}`)
	w, _ = req(t, h, expiredUser, "GET", "/api/invitations/active", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expired active=%d %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceProjectsMembershipInvitesAndColumnProject(t *testing.T) {
	root := t.TempDir()
	a, e := Open(filepath.Join(t.TempDir(), "db"), root)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@x.test","password":"password1"}`)
	_, member := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"member@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Team"}`)
	if w.Code != 201 {
		t.Fatalf("workspace %d %s", w.Code, w.Body.String())
	}
	var x map[string]any
	json.Unmarshal(w.Body.Bytes(), &x)
	wid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/invitations", `{"email":"member@x.test"}`)
	if w.Code != 201 {
		t.Fatalf("invite %d %s", w.Code, w.Body.String())
	}
	var invite map[string]any
	json.Unmarshal(w.Body.Bytes(), &invite)
	tok, _ := invite["token"].(string)
	if tok == "" {
		t.Fatal("test mail seam did not return token")
	}
	w, _ = req(t, h, owner, "GET", "/api/workspaces/"+itoa(wid)+"/members", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"email":"member@x.test"`) || !strings.Contains(w.Body.String(), `"status":"invited"`) {
		t.Fatalf("invited user list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "POST", "/api/invitations/"+tok, "{}")
	if w.Code != 200 {
		t.Fatalf("accept %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "GET", "/api/workspaces/"+itoa(wid)+"/members", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"email":"member@x.test"`) || !strings.Contains(w.Body.String(), `"status":"member"`) || strings.Contains(w.Body.String(), `"status":"invited"`) {
		t.Fatalf("accepted user list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/workspaces", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Team") {
		t.Fatalf("member list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "PATCH", "/api/workspaces/"+itoa(wid), `{"name":"No"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member managed workspace: %d", w.Code)
	}
	projectDir := filepath.Join(root, "app")
	if e = os.Mkdir(projectDir, 0755); e != nil {
		t.Fatal(e)
	}
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/projects", `{"name":"App","directory":"app"}`)
	if w.Code != 201 {
		t.Fatalf("relative project %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &x)
	pid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/projects", `{"name":"Duplicate","directory":"app"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "directory is already used by App") {
		t.Fatalf("duplicate project directory %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "POST", "/api/boards", `{"name":"Board","workspaceId":`+itoa(wid)+`}`)
	json.Unmarshal(w.Body.Bytes(), &x)
	bid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/boards/"+itoa(bid)+"/columns", `{"name":"Todo","projectId":`+itoa(pid)+`,"worktreeEnabled":false}`)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "projectId") {
		t.Fatalf("column %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &x)
	cid := int64(x["id"].(float64))
	w, _ = req(t, h, owner, "POST", "/api/columns/"+itoa(cid)+"/jobs", `{"task":"Fix project search","doneDefinition":"Tests pass"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project job %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/projects", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"App"`) || !strings.Contains(w.Body.String(), `"jobCount":1`) {
		t.Fatalf("member project list %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/projects/"+itoa(pid), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"task":"Fix project search"`) || !strings.Contains(w.Body.String(), `"boardName":"Board"`) || !strings.Contains(w.Body.String(), `"columnName":"Todo"`) {
		t.Fatalf("member project detail %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/boards/"+itoa(bid)+"/columns", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"projectName":"App"`) {
		t.Fatalf("member columns %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "PATCH", "/api/columns/"+itoa(cid), `{"name":"Review","projectId":`+itoa(pid)+`}`)
	if w.Code != 200 {
		t.Fatalf("member column patch %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "GET", "/api/boards/"+itoa(bid)+"/columns", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Review"`) {
		t.Fatalf("column patch did not persist name: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "PATCH", "/api/projects/"+itoa(pid), `{"name":"Renamed","directory":"`+root+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Renamed") {
		t.Fatalf("project patch %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, owner, "DELETE", "/api/workspaces/"+itoa(wid)+"/members/1", "")
	if w.Code != 409 {
		t.Fatalf("self/final owner removal %d", w.Code)
	}
}

func TestWorkspaceMembersCanArchiveColumnsButNotOtherUsersJobs(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"archive-owner@x.test","password":"password1"}`)
	_, member := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"archive-member@x.test","password":"password1"}`)
	_, outsider := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"archive-outsider@x.test","password":"password1"}`)

	var ownerID, memberID, workspaceID, boardID, projectID int64
	if err = a.DB.QueryRow(`SELECT u.id,b.workspace_id,b.id,p.id FROM users u JOIN boards b ON b.user_id=u.id JOIN projects p ON p.workspace_id=b.workspace_id WHERE u.email=?`, "archive-owner@x.test").Scan(&ownerID, &workspaceID, &boardID, &projectID); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow(`SELECT id FROM users WHERE email=?`, "archive-member@x.test").Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member')`, workspaceID, memberID); err != nil {
		t.Fatal(err)
	}

	createColumn := func(name string) (int64, int64) {
		t.Helper()
		w, _ := req(t, h, owner, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"`+name+`","projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
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
		if _, err := a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=?`, laneID); err != nil {
			t.Fatal(err)
		}
		return columnID, laneID
	}
	createJob := func(cookie *http.Cookie, columnID int64) int64 {
		t.Helper()
		w, _ := req(t, h, cookie, "POST", "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"Archive me"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create job: %d %s", w.Code, w.Body.String())
		}
		var job map[string]any
		json.Unmarshal(w.Body.Bytes(), &job)
		return int64(job["id"].(float64))
	}
	assertArchived := func(table string, id int64, want bool) {
		t.Helper()
		var archived bool
		if err := a.DB.QueryRow(`SELECT archived FROM `+table+` WHERE id=?`, id).Scan(&archived); err != nil || archived != want {
			t.Fatalf("%s %d archived=%t, want %t (err=%v)", table, id, archived, want, err)
		}
	}

	columnID, _ := createColumn("Member job archive")
	jobID := createJob(owner, columnID)
	w, _ := req(t, h, outsider, "DELETE", "/api/jobs/"+itoa(jobID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider archived job: %d %s", w.Code, w.Body.String())
	}
	assertArchived("jobs", jobID, false)
	w, _ = req(t, h, member, "DELETE", "/api/jobs/"+itoa(jobID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("member archived owner job: %d %s", w.Code, w.Body.String())
	}
	assertArchived("jobs", jobID, false)

	columnID, _ = createColumn("Owner job archive")
	jobID = createJob(member, columnID)
	w, _ = req(t, h, owner, "DELETE", "/api/jobs/"+itoa(jobID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("owner archived member job: %d %s", w.Code, w.Body.String())
	}
	assertArchived("jobs", jobID, false)

	columnID, _ = createColumn("Member column archive")
	jobID = createJob(owner, columnID)
	w, _ = req(t, h, outsider, "DELETE", "/api/columns/"+itoa(columnID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider archived column: %d %s", w.Code, w.Body.String())
	}
	assertArchived("columns", columnID, false)
	assertArchived("jobs", jobID, false)
	w, _ = req(t, h, member, "DELETE", "/api/columns/"+itoa(columnID), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("member archive column: %d %s", w.Code, w.Body.String())
	}
	assertArchived("columns", columnID, true)
	assertArchived("jobs", jobID, true)
}

func TestWorkspaceMembersCanReadJobsButNotEditThem(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()
	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"read-owner@x.test","password":"password1"}`)
	_, member := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"read-member@x.test","password":"password1"}`)
	_, outsider := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"read-outsider@x.test","password":"password1"}`)

	var memberID, workspaceID, boardID, projectID int64
	if err = a.DB.QueryRow(`SELECT b.workspace_id,b.id,p.id FROM boards b JOIN users u ON u.id=b.user_id JOIN projects p ON p.workspace_id=b.workspace_id WHERE u.email=?`, "read-owner@x.test").Scan(&workspaceID, &boardID, &projectID); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.QueryRow(`SELECT id FROM users WHERE email=?`, "read-member@x.test").Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member')`, workspaceID, memberID); err != nil {
		t.Fatal(err)
	}

	w, _ := req(t, h, owner, "POST", "/api/boards/"+itoa(boardID)+"/columns", `{"name":"Todo","projectId":`+itoa(projectID)+`,"worktreeEnabled":false}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create column: %d %s", w.Code, w.Body.String())
	}
	var column map[string]any
	json.Unmarshal(w.Body.Bytes(), &column)
	columnID := int64(column["id"].(float64))
	if _, err = a.DB.Exec(`UPDATE lanes SET paused=1 WHERE id=(SELECT lane_id FROM columns WHERE id=?)`, columnID); err != nil {
		t.Fatal(err)
	}
	w, _ = req(t, h, owner, "POST", "/api/columns/"+itoa(columnID)+"/jobs", `{"task":"Shared task","doneDefinition":"Reviewed"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", w.Code, w.Body.String())
	}
	var job map[string]any
	json.Unmarshal(w.Body.Bytes(), &job)
	jobID := int64(job["id"].(float64))
	if err = a.appendJobEvent(jobID, "output", "shared timeline entry"); err != nil {
		t.Fatal(err)
	}
	w, _ = req(t, h, member, "GET", "/api/lanes", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"task":"Shared task"`) {
		t.Errorf("member GET /api/lanes: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, outsider, "GET", "/api/lanes", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"task":"Shared task"`) {
		t.Errorf("outsider GET /api/lanes exposed workspace job: %d %s", w.Code, w.Body.String())
	}

	for _, path := range []string{"/api/jobs/" + itoa(jobID), "/api/jobs/" + itoa(jobID) + "/events"} {
		w, _ = req(t, h, member, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Errorf("member GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "shared timeline entry") {
			t.Errorf("member GET %s omitted timeline: %s", path, w.Body.String())
		}
		w, _ = req(t, h, outsider, "GET", path, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("outsider GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w, _ = req(t, h, member, "PATCH", "/api/jobs/"+itoa(jobID), `{"task":"Changed"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("member edited owner job: %d %s", w.Code, w.Body.String())
	}
	w, _ = req(t, h, member, "DELETE", "/api/jobs/"+itoa(jobID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("member archived owner job: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceUsersIncludesPendingInvitationsAndMembers(t *testing.T) {
	root := t.TempDir()
	a, err := Open(filepath.Join(t.TempDir(), "db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()

	_, owner := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner-users@x.test","password":"password1"}`)
	w, _ := req(t, h, owner, "POST", "/api/workspaces", `{"name":"Users Team"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("workspace %d %s", w.Code, w.Body.String())
	}
	var workspace map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	wid := int64(workspace["id"].(float64))

	w, _ = req(t, h, owner, "POST", "/api/workspaces/"+itoa(wid)+"/invitations", `{"email":"pending-users@x.test"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite %d %s", w.Code, w.Body.String())
	}

	w, _ = req(t, h, owner, "GET", "/api/workspaces/"+itoa(wid)+"/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("users %d %s", w.Code, w.Body.String())
	}
	var users []struct {
		Email  string `json:"email"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, user := range users {
		statuses[user.Email] = user.Status
	}
	if statuses["owner-users@x.test"] != "member" {
		t.Fatalf("owner status = %q, users = %s", statuses["owner-users@x.test"], w.Body.String())
	}
	if statuses["pending-users@x.test"] != "invited" {
		t.Fatalf("pending status = %q, users = %s", statuses["pending-users@x.test"], w.Body.String())
	}
}

func TestMigrationCreatesDefaultProjectIdempotently(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	root := t.TempDir()
	a, e := Open(db, root)
	if e != nil {
		t.Fatal(e)
	}
	_, c := req(t, a.Handler(), nil, "POST", "/api/auth/signup", `{"email":"m@x.test","password":"password1"}`)
	_ = c
	a.Close()
	a, e = Open(db, root)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var n int
	if e = a.DB.QueryRow(`SELECT count(*) FROM projects WHERE name='Default Project'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("default projects=%d err=%v", n, e)
	}
}

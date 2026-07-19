package board

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLegacyCardinalityMigrationIsAtomicAndRestartSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE users(id INTEGER PRIMARY KEY,email TEXT UNIQUE NOT NULL,password_hash BLOB NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE workspaces(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,name TEXT NOT NULL,root TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(user_id,root));
CREATE TABLE boards(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users ON DELETE CASCADE,workspace_id INTEGER NOT NULL UNIQUE REFERENCES workspaces ON DELETE RESTRICT,name TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO users(id,email,password_hash) VALUES(7,'legacy@example.test',x'00');
INSERT INTO workspaces(id,user_id,name,root) VALUES(11,7,'legacy','/legacy');
INSERT INTO boards(id,user_id,workspace_id,name) VALUES(13,7,11,'one');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	for pass := 0; pass < 2; pass++ {
		a, err := Open(path, t.TempDir())
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		var workspace, board, broken, scratch int
		if err = a.DB.QueryRow(`SELECT workspace_id,id FROM boards WHERE id=13`).Scan(&workspace, &board); err != nil || workspace != 11 || board != 13 {
			t.Fatalf("data lost: workspace=%d board=%d err=%v", workspace, board, err)
		}
		if err = a.DB.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
			t.Fatalf("foreign keys: %d %v", broken, err)
		}
		if err = a.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE '%_new'`).Scan(&scratch); err != nil || scratch != 0 {
			t.Fatalf("scratch tables: %d %v", scratch, err)
		}
		if _, err = a.DB.Exec(`INSERT INTO workspaces(user_id,name,root) VALUES(7,'duplicate','/legacy')`); err != nil {
			t.Fatalf("workspace cardinality still unique: %v", err)
		}
		if _, err = a.DB.Exec(`INSERT INTO boards(user_id,workspace_id,name) VALUES(7,11,'another')`); err != nil {
			t.Fatalf("board cardinality still unique: %v", err)
		}
		a.Close()
	}
}

func TestStartRevalidatesRuntimeDirectoryBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(*testing.T, string) string
	}{
		{"missing directory", func(t *testing.T, root string) string {
			path := filepath.Join(root, "deleted")
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"symlink retargeted outside root", func(t *testing.T, root string) string {
			inside := filepath.Join(root, "inside")
			if err := os.Mkdir(inside, 0755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "runtime")
			if err := os.Symlink(inside, link); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			a, err := Open(filepath.Join(t.TempDir(), "db"), root)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			directory := tc.path(t, root)
			_, err = a.DB.Exec(`
INSERT INTO users(id,email,password_hash) VALUES(92,'path@example.test',x'00');
INSERT INTO workspaces(id,user_id,name,root) VALUES(92,92,'workspace',?);
INSERT INTO boards(id,user_id,workspace_id,name) VALUES(92,92,92,'board');
INSERT INTO projects(id,user_id,workspace_id,name,directory) VALUES(92,92,92,'project',?);
INSERT INTO lanes(id,user_id,name,position) VALUES(92,92,'lane',92);
INSERT INTO columns(id,user_id,board_id,lane_id,project_id,name,position) VALUES(92,92,92,92,92,'column',92);
INSERT INTO jobs(id,user_id,lane_id,task,position) VALUES(92,92,92,'task',92);`, root, directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.DB.Exec(`UPDATE projects SET directory=? WHERE id=92`, directory); err != nil {
				t.Fatal(err)
			}

			a.start(92, "task", "done", root)

			var state, warning string
			var attempts, runs int
			if err = a.DB.QueryRow(`SELECT state,warning,attempt_count FROM jobs WHERE id=92`).Scan(&state, &warning, &attempts); err != nil {
				t.Fatal(err)
			}
			if err = a.DB.QueryRow(`SELECT count(*) FROM job_runs WHERE job_id=92`).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if state != "todo" || warning != "" || attempts != 0 || runs != 0 {
				t.Fatalf("state=%q warning=%q attempts=%d runs=%d", state, warning, attempts, runs)
			}
		})
	}
}

func TestStartFailsClosedWithoutSelectedProject(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, err = a.DB.Exec(`INSERT INTO users(id,email,password_hash) VALUES(91,'runtime@example.test',x'00'); INSERT INTO lanes(id,user_id,name,position) VALUES(91,91,'lane',91); INSERT INTO jobs(id,user_id,lane_id,task,position) VALUES(91,91,91,'task',91)`)
	if err != nil {
		t.Fatal(err)
	}
	a.start(91, "task", "done", t.TempDir())
	var state, warning string
	if err = a.DB.QueryRow(`SELECT state,warning FROM jobs WHERE id=91`).Scan(&state, &warning); err != nil || state != "blocked" || !strings.Contains(warning, "project") {
		t.Fatalf("state=%q warning=%q err=%v", state, warning, err)
	}
	var runs int
	a.DB.QueryRow(`SELECT count(*) FROM job_runs WHERE job_id=91 AND tmux_session<>'job-history'`).Scan(&runs)
	if runs != 0 {
		t.Fatalf("started %d runs from caller root", runs)
	}
}

func TestInvitationRejectsNonCanonicalAndInjectedAddresses(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Mailer = testMailer{}
	h := a.Handler()
	_, cookie := req(t, h, nil, "POST", "/api/auth/signup", `{"email":"owner@example.test","password":"password1"}`)
	w, _ := req(t, h, cookie, "POST", "/api/workspaces", `{"name":"w","projectDirectory":"`+t.TempDir()+`"}`)
	var wid int64
	if err = a.DB.QueryRow(`SELECT id FROM workspaces ORDER BY id DESC LIMIT 1`).Scan(&wid); err != nil {
		t.Fatal(err)
	}
	path := "/api/workspaces/" + itoa(wid) + "/invitations"
	for _, email := range []string{"victim@example.test\r\nBcc: attacker@example.test", "a@example.test, b@example.test", "Name <a@example.test>", "a@@example.test", "a@example.test trailing"} {
		w, _ = req(t, h, cookie, "POST", path, `{"email":`+strconvQuote(email)+`}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %q: %d %s", email, w.Code, w.Body.String())
		}
	}
}

func strconvQuote(s string) string {
	return `"` + strings.NewReplacer("\\", "\\\\", "\r", "\\r", "\n", "\\n", `"`, `\"`).Replace(s) + `"`
}

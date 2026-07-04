package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/capt4ce/custom-agent/internal/config"
)

func TestFileToolsRespectWorkspaceAndSearchContent(t *testing.T) {
	dir := t.TempDir()
	profile := config.Profile{FileAccess: "workspace", WorkspaceRoots: []string{dir}, EnabledTools: []string{"file_read", "file_create", "file_update", "file_search"}}
	r := NewRegistry(config.Config{})

	created := r.Run(context.Background(), profile, "file_create", raw(map[string]string{"path": filepath.Join(dir, "notes.txt"), "content": "hello world"}))
	if !created.OK {
		t.Fatalf("create failed: %+v", created)
	}
	updated := r.Run(context.Background(), profile, "file_update", raw(map[string]string{"path": filepath.Join(dir, "notes.txt"), "old": "world", "new": "agent"}))
	if !updated.OK {
		t.Fatalf("update failed: %+v", updated)
	}
	read := r.Run(context.Background(), profile, "file_read", raw(map[string]string{"path": filepath.Join(dir, "notes.txt")}))
	if read.Output != "hello agent" {
		t.Fatalf("read = %q", read.Output)
	}
	search := r.Run(context.Background(), profile, "file_search", raw(map[string]string{"query": "agent"}))
	if !strings.Contains(search.Output, "notes.txt") {
		t.Fatalf("search output missing file with content match: %s", search.Output)
	}
	outside := r.Run(context.Background(), profile, "file_create", raw(map[string]string{"path": filepath.Join(t.TempDir(), "x"), "content": "no"}))
	if outside.OK || outside.Error == "" {
		t.Fatalf("outside write should be rejected: %+v", outside)
	}
}

func TestShellApprovalAndCwdPolicy(t *testing.T) {
	dir := t.TempDir()
	profile := config.Profile{FileAccess: "workspace", WorkspaceRoots: []string{dir}, EnabledTools: []string{"shell_run"}}
	r := NewRegistry(config.Config{})
	needsApproval := r.Run(context.Background(), profile, "shell_run", raw(map[string]string{"command": "pwd", "cwd": dir}))
	if needsApproval.Approval == nil {
		t.Fatalf("shell without approval should request approval: %+v", needsApproval)
	}
	ok := r.Run(context.Background(), profile, "shell_run", raw(map[string]any{"command": "pwd", "cwd": dir, "approved": true}))
	if !ok.OK || strings.TrimSpace(ok.Output) != dir {
		t.Fatalf("approved shell did not run in cwd: %+v", ok)
	}
	blocked := r.Run(context.Background(), profile, "shell_run", raw(map[string]any{"command": "pwd", "cwd": filepath.Join(t.TempDir(), "outside"), "approved": true}))
	if blocked.OK || blocked.Error == "" {
		t.Fatalf("outside cwd should be rejected: %+v", blocked)
	}
}

func TestEnabledToolsFilterSchemasAndRuns(t *testing.T) {
	r := NewRegistry(config.Config{})
	profile := config.Profile{FileAccess: "workspace", WorkspaceRoots: []string{t.TempDir()}, EnabledTools: []string{"file_read"}}
	if got := len(r.Schemas(profile)); got != 1 {
		t.Fatalf("schema count = %d", got)
	}
	res := r.Run(context.Background(), profile, "shell_run", raw(map[string]any{"command": "true", "approved": true}))
	if res.OK || !strings.Contains(res.Error, "not enabled") {
		t.Fatalf("disabled tool should fail: %+v", res)
	}
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestMain(m *testing.M) { os.Exit(m.Run()) }

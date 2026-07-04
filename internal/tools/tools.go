package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/capt4ce/custom-agent/internal/config"
	"github.com/capt4ce/custom-agent/internal/llm"
	"github.com/capt4ce/custom-agent/internal/mcp"
)

type ApprovalRequest struct {
	Tool   string `json:"tool"`
	Risk   string `json:"risk"`
	Reason string `json:"reason"`
}

type RunResult struct {
	Tool     string           `json:"tool"`
	OK       bool             `json:"ok"`
	Output   string           `json:"output,omitempty"`
	Error    string           `json:"error,omitempty"`
	Approval *ApprovalRequest `json:"approval,omitempty"`
}

func (r RunResult) JSON() string { b, _ := json.Marshal(r); return string(b) }

type handler func(context.Context, config.Profile, json.RawMessage) RunResult

type Registry struct {
	handlers map[string]handler
	mcp      map[string]mcp.Server
}

func NewRegistry(cfg config.Config) *Registry {
	r := &Registry{handlers: map[string]handler{}, mcp: map[string]mcp.Server{}}
	r.handlers["file_read"] = readFile
	r.handlers["file_create"] = createFile
	r.handlers["file_update"] = updateFile
	r.handlers["file_search"] = searchFiles
	r.handlers["shell_run"] = shellRun
	r.handlers["mcp_call"] = r.mcpCall
	for _, srv := range mcp.ListConfigured(cfg) {
		r.mcp[srv.Config.Name] = srv
	}
	return r
}

func (r *Registry) Schemas(p config.Profile) []llm.ToolSchema {
	obj := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
	schemas := []llm.ToolSchema{
		{Name: "file_read", Description: "Read a UTF-8 text file inside allowed roots", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}},
		{Name: "file_create", Description: "Create or overwrite a UTF-8 text file inside allowed roots", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}},
		{Name: "file_update", Description: "Replace text in an existing UTF-8 text file inside allowed roots", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "old": map[string]any{"type": "string"}, "new": map[string]any{"type": "string"}}, "required": []string{"path", "old", "new"}}},
		{Name: "file_search", Description: "List files by name or content substring inside allowed roots", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}},
		{Name: "shell_run", Description: "Run a shell command. Requires approval in normal use.", Parameters: obj},
	}
	if len(r.mcp) > 0 {
		schemas = append(schemas, llm.ToolSchema{Name: "mcp_call", Description: "Call a configured MCP server by name", Parameters: map[string]any{"type": "object", "properties": map[string]any{"server": map[string]any{"type": "string"}, "payload": map[string]any{"type": "object"}}, "required": []string{"server", "payload"}}})
	}
	if len(p.EnabledTools) == 0 {
		return schemas
	}
	var out []llm.ToolSchema
	for _, schema := range schemas {
		if enabled(p, schema.Name) {
			out = append(out, schema)
		}
	}
	return out
}

func (r *Registry) Run(ctx context.Context, p config.Profile, name string, args json.RawMessage) RunResult {
	if len(p.EnabledTools) > 0 && !enabled(p, name) {
		return RunResult{Tool: name, Error: "tool not enabled: " + name}
	}
	h, ok := r.handlers[name]
	if !ok {
		return RunResult{Tool: name, Error: "unknown tool"}
	}
	if !toolEnabled(p, name) {
		return RunResult{Tool: name, Error: "tool not enabled: " + name}
	}
	return h(ctx, p, args)
}

func toolEnabled(p config.Profile, name string) bool {
	return len(p.EnabledTools) == 0 || slices.Contains(p.EnabledTools, name)
}
func enabled(p config.Profile, name string) bool {
	for _, t := range p.EnabledTools {
		if t == name {
			return true
		}
	}
	return false
}

func decode[T any](raw json.RawMessage) (T, error) { var v T; return v, json.Unmarshal(raw, &v) }

func allowed(p config.Profile, path string) (string, error) {
	if p.FileAccess == "disabled" {
		return "", fmt.Errorf("file access disabled")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if p.FileAccess == "home" {
		home, _ := os.UserHomeDir()
		if inside(abs, home) {
			return abs, nil
		}
		return "", fmt.Errorf("path %s outside home", abs)
	}
	for _, root := range p.WorkspaceRoots {
		if inside(abs, root) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path %s outside allowed roots", abs)
}

func inside(abs, root string) bool {
	ra, _ := filepath.Abs(root)
	return abs == ra || strings.HasPrefix(abs, ra+string(os.PathSeparator))
}

func readFile(_ context.Context, p config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct {
		Path string `json:"path"`
	}](raw)
	if err != nil {
		return RunResult{Tool: "file_read", Error: err.Error()}
	}
	path, err := allowed(p, a.Path)
	if err != nil {
		return RunResult{Tool: "file_read", Error: err.Error()}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RunResult{Tool: "file_read", Error: err.Error()}
	}
	if !utf8.Valid(b) {
		return RunResult{Tool: "file_read", Error: "not a UTF-8 text file"}
	}
	return RunResult{Tool: "file_read", OK: true, Output: string(b)}
}

func createFile(_ context.Context, p config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct{ Path, Content string }](raw)
	if err != nil {
		return RunResult{Tool: "file_create", Error: err.Error()}
	}
	path, err := allowed(p, a.Path)
	if err != nil {
		return RunResult{Tool: "file_create", Error: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return RunResult{Tool: "file_create", Error: err.Error()}
	}
	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return RunResult{Tool: "file_create", Error: err.Error()}
	}
	return RunResult{Tool: "file_create", OK: true, Output: "written " + path}
}

func updateFile(_ context.Context, p config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct{ Path, Old, New string }](raw)
	if err != nil {
		return RunResult{Tool: "file_update", Error: err.Error()}
	}
	path, err := allowed(p, a.Path)
	if err != nil {
		return RunResult{Tool: "file_update", Error: err.Error()}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RunResult{Tool: "file_update", Error: err.Error()}
	}
	s := string(b)
	if !strings.Contains(s, a.Old) {
		return RunResult{Tool: "file_update", Error: "old text not found"}
	}
	if err := os.WriteFile(path, []byte(strings.Replace(s, a.Old, a.New, 1)), 0o644); err != nil {
		return RunResult{Tool: "file_update", Error: err.Error()}
	}
	return RunResult{Tool: "file_update", OK: true, Output: "updated " + path}
}

func searchFiles(_ context.Context, p config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct {
		Query string `json:"query"`
	}](raw)
	if err != nil {
		return RunResult{Tool: "file_search", Error: err.Error()}
	}
	var out []string
	for _, root := range p.WorkspaceRoots {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(path)
			if strings.Contains(filepath.Base(path), a.Query) || (utf8.Valid(b) && strings.Contains(string(b), a.Query)) {
				out = append(out, path)
			}
			return nil
		})
	}
	b, _ := json.Marshal(out)
	return RunResult{Tool: "file_search", OK: true, Output: string(b)}
}

func (r *Registry) mcpCall(ctx context.Context, _ config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct {
		Server  string `json:"server"`
		Payload any    `json:"payload"`
	}](raw)
	if err != nil {
		return RunResult{Tool: "mcp_call", Error: err.Error()}
	}
	srv, ok := r.mcp[a.Server]
	if !ok {
		return RunResult{Tool: "mcp_call", Error: "unknown mcp server: " + a.Server}
	}
	payload, _ := json.Marshal(a.Payload)
	out, err := srv.Call(ctx, payload)
	if err != nil {
		return RunResult{Tool: "mcp_call", Error: err.Error()}
	}
	return RunResult{Tool: "mcp_call", OK: true, Output: string(out)}
}

func shellRun(ctx context.Context, p config.Profile, raw json.RawMessage) RunResult {
	a, err := decode[struct {
		Command, Cwd string
		Approved     bool `json:"approved"`
	}](raw)
	if err != nil {
		return RunResult{Tool: "shell_run", Error: err.Error()}
	}
	if !a.Approved {
		return RunResult{Tool: "shell_run", Approval: &ApprovalRequest{Tool: "shell_run", Risk: "high", Reason: "shell commands require explicit approval"}}
	}
	if a.Cwd != "" {
		if _, err := allowed(p, a.Cwd); err != nil {
			return RunResult{Tool: "shell_run", Error: err.Error()}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", a.Command)
	cmd.Dir = a.Cwd
	b, err := cmd.CombinedOutput()
	if err != nil {
		return RunResult{Tool: "shell_run", Error: err.Error(), Output: string(b)}
	}
	return RunResult{Tool: "shell_run", OK: true, Output: string(b)}
}

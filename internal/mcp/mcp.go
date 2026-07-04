package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"

	"github.com/capt4ce/custom-agent/internal/config"
)

type Server struct{ Config config.MCPServerConfig }
type Tool struct {
	Name   string
	Schema json.RawMessage
}

func ListConfigured(cfg config.Config) []Server {
	out := make([]Server, 0, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		out = append(out, Server{Config: s})
	}
	return out
}

func (s Server) Call(ctx context.Context, payload []byte) ([]byte, error) {
	switch s.Config.Transport {
	case "stdio":
		return s.CallStdio(ctx, payload)
	case "http":
		return s.CallHTTP(ctx, payload)
	default:
		return nil, fmt.Errorf("unsupported mcp transport %q", s.Config.Transport)
	}
}

func (s Server) CallStdio(ctx context.Context, payload []byte) ([]byte, error) {
	if s.Config.Command == "" {
		return nil, fmt.Errorf("mcp stdio command is required")
	}
	cmd := exec.CommandContext(ctx, s.Config.Command, s.Config.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	go func() { defer stdin.Close(); _, _ = stdin.Write(payload) }()
	return cmd.Output()
}

func (s Server) CallHTTP(ctx context.Context, payload []byte) ([]byte, error) {
	if s.Config.URL == "" {
		return nil, fmt.Errorf("mcp http url is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http status %d: %s", res.StatusCode, body)
	}
	return body, nil
}

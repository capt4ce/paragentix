package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/capt4ce/custom-agent/internal/config"
	"github.com/capt4ce/custom-agent/internal/llm"
	"github.com/capt4ce/custom-agent/internal/skills"
	"github.com/capt4ce/custom-agent/internal/storage"
	"github.com/capt4ce/custom-agent/internal/tools"
)

type Request struct{ Profile, SessionID, Input string }
type Response struct {
	SessionID, Output string
	ToolRuns          []tools.RunResult
	RequiresApproval  *tools.ApprovalRequest
}

type Agent struct {
	cfg      config.Config
	db       *storage.DB
	registry *tools.Registry
	provider llm.Provider
}

func New(cfg config.Config, db *storage.DB) *Agent {
	return NewWithProvider(cfg, db, llm.NewProvider(cfg.Model))
}

func NewWithProvider(cfg config.Config, db *storage.DB, provider llm.Provider) *Agent {
	return &Agent{cfg: cfg, db: db, registry: tools.NewRegistry(cfg), provider: provider}
}

func (a *Agent) Run(ctx context.Context, req Request) (Response, error) {
	profileName := req.Profile
	if profileName == "" {
		profileName = "default"
	}
	profile, ok := a.cfg.Profiles[profileName]
	if !ok {
		return Response{}, fmt.Errorf("unknown profile %q", profileName)
	}
	if req.SessionID == "" {
		req.SessionID = fmt.Sprintf("s_%d", time.Now().UnixNano())
		_ = a.db.CreateSession(ctx, storage.Session{ID: req.SessionID, ProfileID: profileName, Source: "agent", Title: req.Input})
	}
	skillText, _ := skills.Load(profile.SkillsDir)
	mems, _ := a.db.ListMemories(ctx, profileName, "")
	messages := []llm.Message{{Role: "system", Content: a.systemPrompt(profile, skillText, mems)}, {Role: "user", Content: req.Input}}
	_, _ = a.db.AppendJSONMessage(ctx, req.SessionID, "user", messages[1])
	toolSchemas := a.registry.Schemas(profile)

	for i := 0; i < 20; i++ {
		out, err := a.provider.Chat(ctx, messages, toolSchemas)
		if err != nil {
			return Response{}, err
		}
		assistantMsg := llm.Message{Role: "assistant", Content: out.Content, ToolCalls: out.ToolCalls}
		messages = append(messages, assistantMsg)
		_, _ = a.db.AppendJSONMessage(ctx, req.SessionID, "assistant", assistantMsg)
		if len(out.ToolCalls) == 0 {
			return Response{SessionID: req.SessionID, Output: out.Content}, nil
		}
		for _, call := range out.ToolCalls {
			res := a.registry.Run(ctx, profile, call.Name, json.RawMessage(call.Arguments))
			if res.Approval != nil {
				return Response{SessionID: req.SessionID, RequiresApproval: res.Approval}, nil
			}
			messages = append(messages, llm.Message{Role: "tool", Name: call.Name, Content: res.JSON()})
		}
	}
	return Response{}, errors.New("agent stopped after max iterations")
}

func (a *Agent) systemPrompt(p config.Profile, skillText string, mems []storage.Memory) string {
	var memoryText []string
	for _, m := range mems {
		memoryText = append(memoryText, "- "+m.Content)
	}
	return strings.TrimSpace(`You are a customizable deployed AI agent.
Use tools when needed. Never invent tool results. Ask for approval before risky actions.
File access policy: ` + p.FileAccess + `

Memory:
` + strings.Join(memoryText, "\n") + `

Loaded skills:
` + skillText)
}

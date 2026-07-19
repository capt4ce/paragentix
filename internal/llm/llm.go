package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/capt4ce/paragentix/internal/config"
)

type Message struct {
	Role      string     `json:"role"`
	Name      string     `json:"name,omitempty"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct{ ID, Name, Arguments string }
type ToolSchema struct {
	Name, Description string
	Parameters        map[string]any
}
type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
}
type Provider interface {
	Chat(context.Context, []Message, []ToolSchema) (ChatResponse, error)
}

func NewProvider(c config.ModelConfig) Provider {
	return OpenAICompatible{Config: c, Client: http.DefaultClient}
}

type OpenAICompatible struct {
	Config config.ModelConfig
	Client *http.Client
}

func (p OpenAICompatible) Chat(ctx context.Context, messages []Message, tools []ToolSchema) (ChatResponse, error) {
	key := os.Getenv(p.Config.APIKeyEnv)
	if key == "" {
		return ChatResponse{Content: "LLM provider is not configured: missing " + p.Config.APIKeyEnv}, nil
	}
	base := strings.TrimRight(p.Config.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body := map[string]any{"model": p.Config.Model, "messages": messages, "tools": openAITools(tools)}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return ChatResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.Client.Do(req)
	if err != nil {
		return ChatResponse{}, err
	}
	defer res.Body.Close()
	var decoded struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string                           `json:"id"`
					Function struct{ Name, Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return ChatResponse{}, err
	}
	if res.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("llm error %d: %v", res.StatusCode, decoded.Error)
	}
	if len(decoded.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("llm returned no choices")
	}
	msg := decoded.Choices[0].Message
	out := ChatResponse{Content: msg.Content}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}

func openAITools(in []ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, s := range in {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": s.Name, "description": s.Description, "parameters": s.Parameters}})
	}
	return out
}

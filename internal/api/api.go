package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/capt4ce/paragentix/internal/agent"
)

func Serve(ctx context.Context, addr string, a *agent.Agent) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, err := a.Run(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	s := &http.Server{Addr: addr, Handler: mux}
	go func() { <-ctx.Done(); _ = s.Shutdown(context.Background()) }()
	return s.ListenAndServe()
}

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/capt4ce/custom-agent/internal/agent"
	"github.com/capt4ce/custom-agent/internal/api"
	"github.com/capt4ce/custom-agent/internal/config"
	"github.com/capt4ce/custom-agent/internal/gateway/discord"
	"github.com/capt4ce/custom-agent/internal/storage"
	"github.com/spf13/cobra"
)

func main() {
	var configPath string
	root := &cobra.Command{Use: "custom-agent"}
	root.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "config file")

	root.AddCommand(&cobra.Command{
		Use:  "chat [prompt]",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := runtime(configPath)
			if err != nil {
				return err
			}
			out, err := r.Agent.Run(cmd.Context(), agent.Request{Profile: "default", Input: args[0]})
			if err != nil {
				return err
			}
			fmt.Println(out.Output)
			return nil
		},
	})

	var addr string
	serve := &cobra.Command{
		Use: "serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := runtime(configPath)
			if err != nil {
				return err
			}
			if r.Config.Discord.Enabled {
				go func() { log.Println(discord.Run(cmd.Context(), r.Config, r.Agent)) }()
			}
			if addr == "" {
				addr = r.Config.Server.Addr
			}
			return api.Serve(cmd.Context(), addr, r.Agent)
		},
	}
	serve.Flags().StringVar(&addr, "addr", "", "listen address")
	root.AddCommand(serve)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

type appRuntime struct {
	Config config.Config
	Agent  *agent.Agent
}

func runtime(path string) (*appRuntime, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	db, err := storage.Open(context.Background(), cfg.Storage.Path)
	if err != nil {
		return nil, err
	}
	return &appRuntime{Config: cfg, Agent: agent.New(cfg, db)}, nil
}

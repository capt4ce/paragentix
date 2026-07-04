package discord

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/capt4ce/custom-agent/internal/agent"
	"github.com/capt4ce/custom-agent/internal/config"
)

func Run(ctx context.Context, cfg config.Config, a *agent.Agent) error {
	token := os.Getenv(cfg.Discord.TokenEnv)
	if token == "" {
		return nil
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, id := range cfg.Discord.AllowedChannelIDs {
		allowed[id] = true
	}
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || (len(allowed) > 0 && !allowed[m.ChannelID]) {
			return
		}
		input := strings.TrimSpace(m.Content)
		if input == "" {
			return
		}
		res, err := a.Run(ctx, agent.Request{Profile: cfg.Discord.DefaultProfile, SessionID: m.ChannelID, Input: input})
		if err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "error: "+err.Error())
			return
		}
		if res.RequiresApproval != nil {
			_, _ = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{Content: "Approval required: " + res.RequiresApproval.Reason, Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{Label: "Approve", Style: discordgo.SuccessButton, CustomID: "approve"}, discordgo.Button{Label: "Deny", Style: discordgo.DangerButton, CustomID: "deny"}}}}})
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, res.Output)
	})
	if err := s.Open(); err != nil {
		return err
	}
	log.Println("discord gateway running")
	<-ctx.Done()
	return s.Close()
}

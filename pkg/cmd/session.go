package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Session persistence lets consecutive `ask <provider> ...` commands in the same
// shell chain into one conversation, so follow-ups ("what's in 1", "its policy")
// resolve against earlier turns. Sessions are keyed by provider + parent shell
// PID (so each terminal is independent) and expire after sessionTTL of idleness.

const (
	sessionTTL      = 30 * time.Minute
	sessionMaxTurns = 12 // cap carried context to bound tokens
)

type sessionTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type sessionData struct {
	Provider string        `json:"provider"`
	Updated  time.Time     `json:"updated"`
	Turns    []sessionTurn `json:"turns"`
}

func sessionPath(provider string) string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "ask", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, provider+"-"+strconv.Itoa(os.Getppid())+".json")
}

// loadSessionTurns returns the prior turns for this provider+shell, or nil if
// there is no session or it has gone stale.
func loadSessionTurns(provider string) []sessionTurn {
	b, err := os.ReadFile(sessionPath(provider))
	if err != nil {
		return nil
	}
	var s sessionData
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	if time.Since(s.Updated) > sessionTTL {
		return nil
	}
	return s.Turns
}

func saveSessionTurns(provider string, turns []sessionTurn) {
	if len(turns) > sessionMaxTurns {
		turns = turns[len(turns)-sessionMaxTurns:]
	}
	b, err := json.MarshalIndent(sessionData{Provider: provider, Updated: time.Now(), Turns: turns}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(sessionPath(provider), b, 0o600)
}

func clearSession(provider string) { _ = os.Remove(sessionPath(provider)) }

// historyFromTurns reconstructs prior turns as alternating user/assistant text
// messages to seed the agent loop's context.
func historyFromTurns(turns []sessionTurn) []anthropic.MessageParam {
	var h []anthropic.MessageParam
	for _, t := range turns {
		if t.User != "" {
			h = append(h, anthropic.NewUserMessage(anthropic.NewTextBlock(t.User)))
		}
		if t.Assistant != "" {
			h = append(h, anthropic.NewAssistantMessage(anthropic.NewTextBlock(t.Assistant)))
		}
	}
	return h
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// chooseBrain resolves the model backend. An explicit value (api/claude-cli)
// wins; "auto" (the default) prefers a logged-in local claude CLI when no
// API key/auth token is configured, else falls back to the API.
func chooseBrain(brainFlag, apiKey, authToken string, claudeAvailable func() bool) string {
	switch b := strings.ToLower(strings.TrimSpace(brainFlag)); b {
	case "auto", "":
		if apiKey != "" || authToken != "" {
			return "api"
		}
		if claudeAvailable() {
			return "claude-cli"
		}
		return "api"
	default:
		return b
	}
}

// claudeCLIAvailable reports whether a logged-in `claude` CLI is usable as the
// model backend: the binary is on PATH and a credential source exists (the
// ~/.claude credentials file, or the macOS keychain item Claude Code uses).
func claudeCLIAvailable() bool {
	bin := os.Getenv("ASK_CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err == nil {
			return true
		}
	}
	if runtime.GOOS == "darwin" {
		if exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil {
			return true
		}
	}
	return false
}

// The claude-cli brain drives the model through the local `claude` binary
// (claude -p) instead of the Anthropic API — the OpenClaw "Claude CLI reuse"
// pattern. It reuses the user's existing Claude login (claude auth), so ask
// needs no API key, OAuth profile, or workspace. Select it with --brain
// claude-cli or ASK_BRAIN=claude-cli.

type claudeDecision struct {
	Args     []string `json:"args"`
	Profiles []string `json:"profiles"`
	Answer   string   `json:"answer"`
}

// claudeCLIMessageFunc adapts `claude -p` to the agent loop's messageFunc.
func claudeCLIMessageFunc() messageFunc {
	bin := os.Getenv("ASK_CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	return func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
		cmd := exec.CommandContext(ctx, bin, "-p",
			"--model", modelToCLI(string(params.Model)),
			"--output-format", "text",
			"--disallowedTools", "Bash,Read,Edit,Write,WebFetch,WebSearch")
		cmd.Stdin = strings.NewReader(renderClaudePrompt(params))
		var out, errBuf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errBuf
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("claude cli (%s): %w: %s", bin, err, strings.TrimSpace(errBuf.String()))
		}
		return decisionToMessage(string(params.Model), parseClaudeDecision(out.String()))
	}
}

// modelToCLI maps an Anthropic model id to a `claude` CLI alias.
func modelToCLI(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "haiku"):
		return "haiku"
	default:
		return "sonnet"
	}
}

// renderClaudePrompt flattens the loop's conversation into a transcript plus an
// output contract claude can follow (next command, or final answer).
func renderClaudePrompt(params anthropic.MessageNewParams) string {
	var sys strings.Builder
	for _, s := range params.System {
		sys.WriteString(s.Text)
		sys.WriteByte('\n')
	}
	var tr strings.Builder
	for _, m := range params.Messages {
		for _, b := range m.Content {
			switch {
			case b.OfText != nil:
				if m.Role == anthropic.MessageParamRoleUser {
					fmt.Fprintf(&tr, "REQUEST: %s\n", b.OfText.Text)
				} else {
					fmt.Fprintf(&tr, "NOTE: %s\n", b.OfText.Text)
				}
			case b.OfToolUse != nil:
				raw, _ := json.Marshal(b.OfToolUse.Input)
				fmt.Fprintf(&tr, "RAN: %s\n", raw)
			case b.OfToolResult != nil:
				var o strings.Builder
				for _, c := range b.OfToolResult.Content {
					if c.OfText != nil {
						o.WriteString(c.OfText.Text)
					}
				}
				fmt.Fprintf(&tr, "OUTPUT:\n%s\n", o.String())
			}
		}
	}
	return strings.Join([]string{
		"You are the planning model inside a CLI agent loop. Follow the SYSTEM rules below.",
		"Decide the SINGLE next step from the transcript.",
		"Reply with ONE json object on the final line and nothing after it:",
		`  {"args":["..."],"profiles":["..."]}  to run the provider CLI (profiles optional), or`,
		`  {"answer":"..."}                      when you can answer the original request.`,
		"",
		"=== SYSTEM ===",
		strings.TrimSpace(sys.String()),
		"",
		"=== TRANSCRIPT ===",
		strings.TrimSpace(tr.String()),
	}, "\n")
}

// parseClaudeDecision extracts the decision JSON from claude's reply: it tries
// the last lines first, then the outermost object, then treats the whole reply
// as a final answer.
func parseClaudeDecision(out string) claudeDecision {
	try := func(s string) (claudeDecision, bool) {
		var d claudeDecision
		if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &d); err == nil && (len(d.Args) > 0 || d.Answer != "") {
			return d, true
		}
		return claudeDecision{}, false
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if d, ok := try(lines[i]); ok {
			return d
		}
	}
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		if d, ok := try(out[i : j+1]); ok {
			return d
		}
	}
	return claudeDecision{Answer: strings.TrimSpace(out)}
}

// decisionToMessage renders a decision as an assistant Message the agent loop
// understands: a run_command tool_use, or an end_turn text answer.
func decisionToMessage(model string, d claudeDecision) (*anthropic.Message, error) {
	if len(d.Args) > 0 {
		input := map[string]any{"args": d.Args}
		if len(d.Profiles) > 0 {
			input["profiles"] = d.Profiles
		}
		return buildAssistantMessage(model, "tool_use", []map[string]any{
			{"type": "tool_use", "id": "toolu_claudecli", "name": runToolName, "input": input},
		})
	}
	return buildAssistantMessage(model, "end_turn", []map[string]any{
		{"type": "text", "text": d.Answer},
	})
}

func buildAssistantMessage(model, stop string, content []map[string]any) (*anthropic.Message, error) {
	body, err := json.Marshal(map[string]any{
		"id": "msg_claudecli", "type": "message", "role": "assistant",
		"model": model, "stop_reason": stop, "content": content,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		return nil, err
	}
	var m anthropic.Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

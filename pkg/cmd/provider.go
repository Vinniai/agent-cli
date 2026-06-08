package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/urfave/cli/v3"
)

// Access classifies the side-effect level of a provider sub-command. Read
// operations run freely; write operations require confirmation; unknown
// operations are treated conservatively (like writes) at the gate.
type Access int

const (
	AccessRead Access = iota
	AccessWrite
	AccessUnknown
)

// AccountContext is a single addressable account/profile for a provider. For
// AWS this maps to a named profile in ~/.aws/config or ~/.aws/credentials.
//   - ID is the value threaded into the CLI invocation (e.g. --profile <ID>);
//     the implicit default profile has an empty ID.
//   - Label is a human-readable name (the default profile is "default").
//   - Source is a coarse origin tag ("profile", "sso", ...).
type AccountContext struct {
	ID     string
	Label  string
	Source string
}

// Provider abstracts a single backing CLI (first implementation: AWS).
type Provider interface {
	Name() string
	Binary() string
	SystemPrompt(ctxs []AccountContext) string
	Classify(args []string) Access
	EnumerateContexts() ([]AccountContext, error)
	// ContextArgs returns extra CLI args used to target a context (e.g. AWS's
	// "--profile X"). ContextEnv returns extra KEY=VALUE environment entries
	// used to target a context (e.g. gh's "GH_HOST=..."). A provider uses
	// whichever mechanism its CLI supports; the other returns nil.
	ContextArgs(c AccountContext) []string
	ContextEnv(c AccountContext) []string
	// DefaultContext returns a target derived from the environment (e.g. AWS's
	// AWS_PROFILE) to use when the user passes no --profile/--all-profiles flag
	// and the model infers none. Returns false when there is no env default.
	DefaultContext() (AccountContext, bool)
}

// commandRunner is the seam used to execute the provider binary. Tests inject a
// fake; the production default shells out via os/exec. env carries extra
// KEY=VALUE entries appended to the process environment for context targeting.
type commandRunner func(ctx context.Context, name string, args []string, env []string) (string, error)

// messageFunc is the seam used to produce an Anthropic response. Tests inject a
// scripted sequence; the production default calls the Anthropic client.
type messageFunc func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)

// defaultCommandRunner runs the binary via os/exec and returns combined output.
func defaultCommandRunner(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// defaultModel is the model used by the assistant agent loop. Haiku is fast and
// cheap for command translation; override per call with --model.
const defaultModel = "claude-haiku-4-5-20251001"

// modelLadder orders assistant models cheapest/fastest -> most capable. Auto-
// routing starts at the chosen model and escalates up this ladder when a call
// fails transiently or a task turns out to be deeply multi-step.
var modelLadder = []string{
	"claude-haiku-4-5-20251001",
	"claude-sonnet-4-6",
	"claude-opus-4-8",
}

// complexStepThreshold is how many tool steps a task may take at one model
// before auto-routing bumps it to the next, more capable model.
const complexStepThreshold = 3

// nextModel returns the next more-capable model after cur, if cur is on the
// ladder and not already at the top.
func nextModel(cur string) (string, bool) {
	for i, m := range modelLadder {
		if m == cur && i+1 < len(modelLadder) {
			return modelLadder[i+1], true
		}
	}
	return cur, false
}

// retryableStatus reports the HTTP status of an API error and whether it is
// worth retrying on a stronger model (rate limits, overload, transient 5xx).
func retryableStatus(err error) (int, bool) {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	switch apiErr.StatusCode {
	case 429, 500, 502, 503, 529:
		return apiErr.StatusCode, true
	}
	return apiErr.StatusCode, false
}

// runToolName is the name of the single tool exposed to the model.
const runToolName = "run_command"

// runCommandInput is the JSON shape the model produces for the run_command tool.
type runCommandInput struct {
	Args     []string `json:"args"`
	Profiles []string `json:"profiles"`
}

// agentLoop holds the injectable seams + state for a single assistant session.
// The command Action builds a default agentLoop; tests construct one directly.
type agentLoop struct {
	provider Provider
	model    string
	runner   commandRunner
	message  messageFunc
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	// contexts are the account contexts the loop fans commands out across when
	// the model does not name explicit profiles.
	contexts []AccountContext
	// available is the full set of account contexts advertised to the model in
	// the system prompt, so it can INFER a target account from the question
	// (e.g. "buckets in prod" -> profiles:["prod"]). Distinct from contexts:
	// available is informational; contexts is the default execution target.
	available []AccountContext
	// forceContexts pins execution to contexts (set by --profile/--all-profiles),
	// overriding any profiles the model infers. When false, model-inferred
	// profiles win, then contexts, then the implicit default.
	forceContexts bool
	// yes bypasses the write-confirmation gate.
	yes bool
	// escalate enables auto-routing up modelLadder on transient failures and
	// deeply multi-step tasks.
	escalate bool
	// history carries the running conversation across run() calls so REPL
	// follow-ups can reference earlier turns ("what's in it", "its policy").
	history []anthropic.MessageParam
	// lastAnswer holds the model's final text from the most recent run(), used
	// to persist the turn for cross-invocation session continuity.
	lastAnswer string
}

// runTool defines the single run_command tool exposed to the model.
func runTool() []anthropic.ToolUnionParam {
	tu := anthropic.ToolUnionParamOfTool(anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"args":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"profiles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"args"},
	}, runToolName)
	tu.OfTool.Description = anthropic.String("Run the provider CLI with the given argument vector.")
	return []anthropic.ToolUnionParam{tu}
}

// confirm prints prompt to out, then reads a y/N answer from stdin. Returns
// true only for an explicit yes.
func confirm(stdin io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	_ = err
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	switch line[0] {
	case 'y', 'Y':
		return true
	default:
		return false
	}
}

// resolveContexts determines which contexts a tool invocation runs against.
// Precedence:
//  1. forceContexts (user pinned --profile/--all-profiles) -> always a.contexts.
//  2. model-inferred profiles from the question -> each becomes a context.
//  3. the loop's configured default contexts.
//  4. a single empty (default) context so the command runs once.
func (a *agentLoop) resolveContexts(in runCommandInput) []AccountContext {
	if a.forceContexts && len(a.contexts) > 0 {
		return a.contexts
	}
	if len(in.Profiles) > 0 {
		ctxs := make([]AccountContext, 0, len(in.Profiles))
		for _, p := range in.Profiles {
			ctxs = append(ctxs, AccountContext{ID: p, Label: p, Source: "profile"})
		}
		return ctxs
	}
	if len(a.contexts) > 0 {
		return a.contexts
	}
	return []AccountContext{{}}
}

// executeTool runs a single run_command tool invocation across the resolved
// contexts, applying the write-confirmation gate. It returns the tool result
// string and whether it should be reported as an error block.
func (a *agentLoop) executeTool(ctx context.Context, in runCommandInput) (string, bool) {
	access := a.provider.Classify(in.Args)
	if access != AccessRead && !a.yes {
		// Confirmation prompt goes to stderr so stdout stays reserved for the
		// model's final answer (which remains cleanly pipeable).
		prompt := fmt.Sprintf("About to run a %s command: %s %s\nProceed? [y/N] ",
			accessLabel(access), a.provider.Binary(), joinArgs(in.Args))
		if !confirm(a.stdin, a.stderr, prompt) {
			fmt.Fprintln(a.stderr, "Skipped (declined).")
			return "Command not executed: user declined confirmation.", false
		}
	}

	contexts := a.resolveContexts(in)
	var b strings.Builder
	anyErr := false
	for _, c := range contexts {
		args := append([]string{}, in.Args...)
		if ctxArgs := a.provider.ContextArgs(c); len(ctxArgs) > 0 {
			args = append(args, ctxArgs...)
		}
		env := a.provider.ContextEnv(c)
		label := c.Label
		if label == "" {
			label = "default"
		}

		// Transparency: echo the exact command being executed to stderr so the
		// user can see what ran (including any context env prefix), then echo
		// its output. stdout is left for the model's summary; the returned
		// string is what the model sees as the tool result.
		envPrefix := ""
		if len(env) > 0 {
			envPrefix = strings.Join(env, " ") + " "
		}
		if len(contexts) > 1 {
			fmt.Fprintf(a.stderr, "$ %s%s %s   [profile: %s]\n", envPrefix, a.provider.Binary(), joinArgs(args), label)
		} else {
			fmt.Fprintf(a.stderr, "$ %s%s %s\n", envPrefix, a.provider.Binary(), joinArgs(args))
		}

		out, err := a.runner(ctx, a.provider.Binary(), args, env)

		if out != "" {
			fmt.Fprint(a.stderr, out)
			if !strings.HasSuffix(out, "\n") {
				fmt.Fprintln(a.stderr)
			}
		}

		if len(contexts) > 1 {
			fmt.Fprintf(&b, "=== profile: %s ===\n", label)
		}
		b.WriteString(out)
		if err != nil {
			anyErr = true
			fmt.Fprintf(a.stderr, "[error: %v]\n", err)
			fmt.Fprintf(&b, "\n[error: %v]\n", err)
		}
		if !strings.HasSuffix(out, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), anyErr
}

func accessLabel(a Access) string {
	switch a {
	case AccessWrite:
		return "write"
	case AccessUnknown:
		return "potentially destructive"
	default:
		return "read"
	}
}

// run drives the agent loop for a single prompt to completion.
func (a *agentLoop) run(ctx context.Context, prompt string) error {
	sys := a.provider.SystemPrompt(a.available)
	model := a.model
	if model == "" {
		model = defaultModel
	}

	// Seed from prior turns so REPL follow-ups keep context, then add this prompt.
	msgs := append([]anthropic.MessageParam{}, a.history...)
	msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))
	tools := runTool()
	steps := 0 // tool steps taken at the current model (drives complexity routing)

	for {
		params := anthropic.MessageNewParams{
			MaxTokens: 4096,
			Model:     anthropic.Model(model),
			System:    []anthropic.TextBlockParam{{Text: sys}},
			Messages:  msgs,
			Tools:     tools,
		}
		resp, err := a.message(ctx, params)
		if err != nil {
			// Auto-route: on a transient failure, retry the same step on the
			// next, more capable model before giving up.
			if a.escalate {
				if code, ok := retryableStatus(err); ok {
					if nm, up := nextModel(model); up {
						fmt.Fprintf(a.stderr, "↑ %s failed (HTTP %d); routing to %s and retrying\n", model, code, nm)
						model, steps = nm, 0
						continue
					}
				}
			}
			if hint := authHint(err); hint != "" {
				fmt.Fprint(a.stderr, hint)
			}
			return err
		}

		var toolResults []anthropic.ContentBlockParamUnion
		var turnText strings.Builder
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.TextBlock:
				if b.Text != "" {
					fmt.Fprintln(a.stdout, b.Text)
					turnText.WriteString(b.Text)
				}
			case anthropic.ToolUseBlock:
				var in runCommandInput
				if err := json.Unmarshal([]byte(b.Input), &in); err != nil {
					toolResults = append(toolResults,
						anthropic.NewToolResultBlock(b.ID, fmt.Sprintf("invalid tool input: %v", err), true))
					continue
				}
				out, isErr := a.executeTool(ctx, in)
				toolResults = append(toolResults, anthropic.NewToolResultBlock(b.ID, out, isErr))
			}
		}

		msgs = append(msgs, resp.ToParam())

		if resp.StopReason != anthropic.StopReasonToolUse {
			a.lastAnswer = turnText.String()
			a.history = msgs
			return nil
		}
		if len(toolResults) == 0 {
			a.lastAnswer = turnText.String()
			a.history = msgs
			return nil
		}
		msgs = append(msgs, anthropic.NewUserMessage(toolResults...))

		// Auto-route: a task still looping after several steps is complex enough
		// to warrant a stronger model for the remaining steps.
		steps++
		if a.escalate && steps >= complexStepThreshold {
			if nm, up := nextModel(model); up {
				fmt.Fprintf(a.stderr, "↑ multi-step task (%d steps); routing %s -> %s\n", steps, model, nm)
				model, steps = nm, 0
			}
		}
	}
}

// defaultMessageFunc returns a messageFunc backed by the Anthropic client.
func defaultMessageFunc(client anthropic.Client) messageFunc {
	return func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
		return client.Messages.New(ctx, params)
	}
}

// authHint returns actionable guidance for model-endpoint auth failures (401/403)
// and rate limits (429), or "" for unrelated errors. The model call — not the
// backing CLI — is what authenticates against the Anthropic API, so these point
// at ask's auth, not aws/gh creds.
func authHint(err error) string {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return ""
	}
	const ways = `
How to authenticate the model:
   ask auth login                          # log in with your Claude account (OAuth)
   export ANTHROPIC_API_KEY=sk-ant-...     # use an API key (separate limits/billing)
   ask --base-url https://ai-gateway.vercel.sh --auth-token "$AI_GATEWAY_API_KEY" ...
                                           # route via an LLM gateway (Bearer token)
   ask auth status                         # show the credential currently in use
`
	switch apiErr.StatusCode {
	case 429:
		return "\nRate limited (HTTP 429): authenticated, but over your limit. Wait and retry,\n" +
			"or switch to a credential with separate limits (API key or a gateway).\n" + ways
	case 401, 403:
		return fmt.Sprintf("\nNot authenticated to the model endpoint (HTTP %d). Log in or set a key.\n%s", apiErr.StatusCode, ways)
	}
	return ""
}

// newProviderCommand builds the cli.Command for a provider.
func newProviderCommand(p Provider) cli.Command {
	name := p.Name()
	return cli.Command{
		Name:      name,
		Category:  "ASSISTANT",
		Usage:     fmt.Sprintf("Natural-language frontend for the %s CLI", name),
		ArgsUsage: "[prompt]",
		Description: fmt.Sprintf(`Ask the %s CLI in plain English. Read commands run immediately; writes print the
command and wait for y/N (use --yes to skip). With no prompt, opens a REPL.
The executed command and its output go to stderr; the final answer to stdout.

   ask %s "list my ..."
   ask %s --profile NAME "..."     # pin one account/host (overrides inference)
   ask %s --all-profiles "..."     # fan out across every account/host
   ask %s "... in prod"            # account inferred from the wording`,
			name, name, name, name, name),
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "model", Value: defaultModel, Usage: "Model that performs the translation"},
			&cli.StringFlag{Name: "profile", Usage: "Pin one account/profile (authoritative; overrides any inferred target)"},
			&cli.BoolFlag{Name: "all-profiles", Usage: "Fan the question out across every profile/host"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip the write-confirmation gate (run writes without asking)"},
			&cli.BoolFlag{Name: "no-escalate", Usage: "Disable auto-routing to a stronger model on transient errors or deeply multi-step tasks"},
			&cli.BoolFlag{Name: "new", Usage: "Start a fresh conversation (ignore the chained session from earlier commands)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client := anthropic.NewClient(getDefaultRequestOptions(cmd.Root())...)

			// Always enumerate the available accounts (best-effort) so the model
			// can INFER a target from the question. Explicit flags pin execution.
			all, _ := p.EnumerateContexts()

			var contexts []AccountContext
			force := false
			switch {
			case cmd.Bool("all-profiles"):
				contexts = all
				force = true
			case cmd.String("profile") != "":
				profile := cmd.String("profile")
				contexts = []AccountContext{{ID: profile, Label: profile, Source: "profile"}}
				force = true
			default:
				// No flag: fall back to an env default (e.g. AWS_PROFILE) so the
				// user need not pass --profile. Non-forced, so the model can still
				// infer a different account from the wording.
				if dc, ok := p.DefaultContext(); ok {
					contexts = []AccountContext{dc}
				}
			}

			available := all
			if force {
				available = contexts
			}

			loop := &agentLoop{
				provider:      p,
				model:         cmd.String("model"),
				runner:        defaultCommandRunner,
				message:       defaultMessageFunc(client),
				stdin:         os.Stdin,
				stdout:        os.Stdout,
				stderr:        os.Stderr,
				contexts:      contexts,
				available:     available,
				forceContexts: force,
				yes:           cmd.Bool("yes"),
				escalate:      !cmd.Bool("no-escalate"),
			}

			args := cmd.Args().Slice()
			if len(args) > 0 {
				prompt := strings.Join(args, " ")
				// Chain onto the recent conversation in this shell so follow-ups
				// keep context across separate `ask` commands. --new resets it.
				if cmd.Bool("new") {
					clearSession(p.Name())
				}
				turns := loadSessionTurns(p.Name())
				loop.history = historyFromTurns(turns)
				err := loop.run(ctx, prompt)
				if err == nil {
					saveSessionTurns(p.Name(), append(turns, sessionTurn{User: prompt, Assistant: loop.lastAnswer}))
				}
				return err
			}
			if isInputPiped() {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				prompt := strings.TrimSpace(string(data))
				if prompt == "" {
					return nil
				}
				return loop.run(ctx, prompt)
			}

			// REPL mode.
			reader := bufio.NewReader(os.Stdin)
			for {
				fmt.Fprintf(os.Stdout, "%s> ", p.Name())
				line, err := reader.ReadString('\n')
				line = strings.TrimSpace(line)
				if line != "" {
					if line == "exit" || line == "quit" {
						return nil
					}
					if rerr := loop.run(ctx, line); rerr != nil {
						fmt.Fprintf(os.Stderr, "error: %v\n", rerr)
					}
				}
				if err != nil {
					return nil
				}
			}
		},
	}
}

// joinArgs is a small helper for diagnostics.
func joinArgs(args []string) string { return strings.Join(args, " ") }

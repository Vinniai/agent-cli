package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// --- registration -----------------------------------------------------------

func TestProviderRegistration(t *testing.T) {
	root := Command
	if root.Name != "ask" {
		t.Fatalf("root command Name = %q, want %q", root.Name, "ask")
	}

	var aws *struct {
		Name     string
		Category string
	}
	for _, sub := range root.Commands {
		if sub.Name == "aws" {
			aws = &struct {
				Name     string
				Category string
			}{Name: sub.Name, Category: sub.Category}
			break
		}
	}
	if aws == nil {
		t.Fatalf("no \"aws\" subcommand registered on root")
	}
	if aws.Category != "ASSISTANT" {
		t.Fatalf("aws subcommand Category = %q, want %q", aws.Category, "ASSISTANT")
	}
}

// --- scripted message helper ------------------------------------------------

// scriptedMessages returns a messageFunc that yields the provided messages in
// order, one per call.
func scriptedMessages(t *testing.T, msgs ...*anthropic.Message) messageFunc {
	t.Helper()
	var i int
	var mu sync.Mutex
	return func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(msgs) {
			t.Fatalf("messageFunc called more times than scripted (%d)", len(msgs))
		}
		m := msgs[i]
		i++
		return m, nil
	}
}

// toolUseMessage builds an assistant Message containing a single run_command
// ToolUseBlock with stop_reason tool_use.
func toolUseMessage(t *testing.T, id string, in runCommandInput) *anthropic.Message {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id":          "msg_" + id,
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-6",
		"stop_reason": "tool_use",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    id,
				"name":  runToolName,
				"input": json.RawMessage(raw),
			},
		},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m anthropic.Message
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal tool_use message: %v", err)
	}
	return &m
}

// textMessage builds an assistant Message with a single TextBlock and
// stop_reason end_turn.
func textMessage(t *testing.T, text string) *anthropic.Message {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":          "msg_text",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-6",
		"stop_reason": "end_turn",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m anthropic.Message
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal text message: %v", err)
	}
	return &m
}

// recordingRunner records each invocation and returns a canned output.
type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
	envs  [][]string
	out   string
}

func (r *recordingRunner) run(ctx context.Context, name string, args []string, env []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	out := r.out
	if out == "" {
		out = "{}"
	}
	return out, nil
}

// --- agent loop: READ command runs and final text is printed ----------------

func TestAgentLoopReadCommand(t *testing.T) {
	runner := &recordingRunner{out: `{"Buckets":[]}`}
	var stdout bytes.Buffer

	loop := &agentLoop{
		provider: awsProvider{},
		model:    defaultModel,
		runner:   runner.run,
		message: scriptedMessages(t,
			toolUseMessage(t, "tool1", runCommandInput{Args: []string{"s3api", "list-buckets"}}),
			textMessage(t, "You have no buckets."),
		),
		stdin:    strings.NewReader(""),
		stdout:   &stdout,
		stderr:   &bytes.Buffer{},
		contexts: nil,
		yes:      false,
	}

	if err := loop.run(context.Background(), "list my buckets"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("runner called %d times, want 1; calls=%v", len(runner.calls), runner.calls)
	}
	want := []string{"aws", "s3api", "list-buckets"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("runner call = %v, want %v", runner.calls[0], want)
	}
	if !strings.Contains(stdout.String(), "You have no buckets.") {
		t.Fatalf("stdout = %q, want it to contain final text", stdout.String())
	}
}

// --- transparency: executed command + output echoed to stderr ---------------

func TestAgentLoopEchoesCommand(t *testing.T) {
	runner := &recordingRunner{out: `{"Buckets":[{"Name":"demo"}]}`}
	var stdout, stderr bytes.Buffer

	loop := &agentLoop{
		provider: awsProvider{},
		model:    defaultModel,
		runner:   runner.run,
		message: scriptedMessages(t,
			toolUseMessage(t, "e1", runCommandInput{Args: []string{"s3api", "list-buckets"}}),
			textMessage(t, "One bucket: demo."),
		),
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &stderr,
	}

	if err := loop.run(context.Background(), "list buckets"); err != nil {
		t.Fatalf("run: %v", err)
	}

	es := stderr.String()
	if !strings.Contains(es, "aws s3api list-buckets") {
		t.Fatalf("stderr should echo the executed command, got %q", es)
	}
	if !strings.Contains(es, "demo") {
		t.Fatalf("stderr should echo the command output, got %q", es)
	}
	// stdout stays reserved for the model's final answer (pipeable).
	if !strings.Contains(stdout.String(), "One bucket: demo.") {
		t.Fatalf("stdout should contain the model answer, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "aws s3api list-buckets") {
		t.Fatalf("the executed command must not leak into stdout, got %q", stdout.String())
	}
}

// --- confirmation gate: WRITE command ---------------------------------------

func TestAgentLoopConfirmGate(t *testing.T) {
	writeInput := runCommandInput{Args: []string{"s3api", "create-bucket", "--bucket", "x"}}

	t.Run("declined", func(t *testing.T) {
		runner := &recordingRunner{}
		var stdout bytes.Buffer
		loop := &agentLoop{
			provider: awsProvider{},
			model:    defaultModel,
			runner:   runner.run,
			message: scriptedMessages(t,
				toolUseMessage(t, "w1", writeInput),
				textMessage(t, "Cancelled."),
			),
			stdin:  strings.NewReader("n\n"),
			stdout: &stdout,
			stderr: &bytes.Buffer{},
			yes:    false,
		}
		if err := loop.run(context.Background(), "create a bucket"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner called %d times on decline, want 0", len(runner.calls))
		}
	})

	t.Run("confirmed via yes flag", func(t *testing.T) {
		runner := &recordingRunner{}
		var stdout bytes.Buffer
		loop := &agentLoop{
			provider: awsProvider{},
			model:    defaultModel,
			runner:   runner.run,
			message: scriptedMessages(t,
				toolUseMessage(t, "w2", writeInput),
				textMessage(t, "Created."),
			),
			stdin:  strings.NewReader(""),
			stdout: &stdout,
			stderr: &bytes.Buffer{},
			yes:    true,
		}
		if err := loop.run(context.Background(), "create a bucket"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("runner called %d times with --yes, want 1", len(runner.calls))
		}
	})

	t.Run("confirmed via stdin y", func(t *testing.T) {
		runner := &recordingRunner{}
		var stdout bytes.Buffer
		loop := &agentLoop{
			provider: awsProvider{},
			model:    defaultModel,
			runner:   runner.run,
			message: scriptedMessages(t,
				toolUseMessage(t, "w3", writeInput),
				textMessage(t, "Created."),
			),
			stdin:  strings.NewReader("y\n"),
			stdout: &stdout,
			stderr: &bytes.Buffer{},
			yes:    false,
		}
		if err := loop.run(context.Background(), "create a bucket"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("runner called %d times on stdin y, want 1", len(runner.calls))
		}
	})
}

// --- account inference + explicit-flag override -----------------------------

// When no flag pins the account, a profile the model infers from the question
// is used (single-profile inference; the multi-profile case is TestAgentLoopFanOut).
func TestAgentLoopInferredProfile(t *testing.T) {
	runner := &recordingRunner{out: `{"Buckets":[]}`}
	loop := &agentLoop{
		provider: awsProvider{},
		model:    defaultModel,
		runner:   runner.run,
		message: scriptedMessages(t,
			toolUseMessage(t, "i1", runCommandInput{Args: []string{"s3api", "list-buckets"}, Profiles: []string{"prod"}}),
			textMessage(t, "Done."),
		),
		stdin:  strings.NewReader(""),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		// available advertises options; not pinned -> model's choice wins.
		available:     []AccountContext{{ID: "prod"}, {ID: "dev"}},
		forceContexts: false,
	}
	if err := loop.run(context.Background(), "list buckets in prod"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("want 1 call, got %d: %v", len(runner.calls), runner.calls)
	}
	if got := strings.Join(runner.calls[0], " "); !strings.Contains(got, "--profile prod") {
		t.Fatalf("expected --profile prod, got %v", runner.calls[0])
	}
}

// When --profile/--all-profiles pins the account, the model cannot override it.
func TestAgentLoopProfileOverride(t *testing.T) {
	runner := &recordingRunner{out: `{"Buckets":[]}`}
	loop := &agentLoop{
		provider: awsProvider{},
		model:    defaultModel,
		runner:   runner.run,
		message: scriptedMessages(t,
			// model tries to target "dev", but the user pinned "prod".
			toolUseMessage(t, "o1", runCommandInput{Args: []string{"s3api", "list-buckets"}, Profiles: []string{"dev"}}),
			textMessage(t, "Done."),
		),
		stdin:         strings.NewReader(""),
		stdout:        &bytes.Buffer{},
		stderr:        &bytes.Buffer{},
		contexts:      []AccountContext{{ID: "prod", Label: "prod", Source: "profile"}},
		forceContexts: true,
	}
	if err := loop.run(context.Background(), "list buckets in dev"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("want 1 call, got %d: %v", len(runner.calls), runner.calls)
	}
	got := strings.Join(runner.calls[0], " ")
	if !strings.Contains(got, "--profile prod") || strings.Contains(got, "dev") {
		t.Fatalf("pinned profile must win: want --profile prod, got %v", runner.calls[0])
	}
}

// --- fan-out across profiles ------------------------------------------------

func TestAgentLoopFanOut(t *testing.T) {
	runner := &recordingRunner{out: `{"Buckets":[]}`}
	var stdout bytes.Buffer

	loop := &agentLoop{
		provider: awsProvider{},
		model:    defaultModel,
		runner:   runner.run,
		message: scriptedMessages(t,
			toolUseMessage(t, "f1", runCommandInput{
				Args:     []string{"s3api", "list-buckets"},
				Profiles: []string{"a", "b"},
			}),
			textMessage(t, "Done."),
		),
		stdin:  strings.NewReader(""),
		stdout: &stdout,
		stderr: &bytes.Buffer{},
		yes:    false,
	}

	if err := loop.run(context.Background(), "list buckets in a and b"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("runner called %d times, want 2 (one per profile); calls=%v", len(runner.calls), runner.calls)
	}

	var sawA, sawB bool
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "--profile a") {
			sawA = true
		}
		if strings.Contains(joined, "--profile b") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected calls with --profile a and --profile b; got %v", runner.calls)
	}
}

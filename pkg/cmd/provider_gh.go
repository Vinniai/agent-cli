package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ghProvider is the GitHub CLI (`gh`) Provider implementation.
type ghProvider struct{}

func (ghProvider) Name() string   { return "gh" }
func (ghProvider) Binary() string { return "gh" }

func (ghProvider) SystemPrompt(ctxs []AccountContext) string {
	var b strings.Builder
	b.WriteString("You are an assistant that translates natural-language requests into GitHub CLI (gh) commands.\n")
	b.WriteString("Use the run_command tool to execute commands. Pass the gh argument vector WITHOUT the leading 'gh'.\n")
	b.WriteString("Prefer high-level commands (gh repo/pr/issue/run/release/...) and fall back to `gh api` for anything else.\n")
	if len(ctxs) > 0 {
		b.WriteString("\nAvailable GitHub hosts (accounts):\n")
		for _, c := range ctxs {
			b.WriteString(fmt.Sprintf("- %s (%s)\n", c.Label, c.Source))
		}
		b.WriteString("\nHost selection: infer the target from the request and set the run_command 'profiles' field to the matching host(s). " +
			"If the user names a host/org tied to a host, pick it; if they say \"all hosts\", include every host above; " +
			"otherwise omit 'profiles' to use the default host.\n")
	}
	return b.String()
}

// ghReadVerbs / ghWriteVerbs classify the second token of a gh command
// (gh <noun> <verb>, e.g. `gh repo list`). Read verbs are safe to run without
// confirmation; write verbs mutate remote (or local) state.
var ghReadVerbs = map[string]bool{
	"list": true, "view": true, "status": true, "diff": true, "checks": true,
	"ls": true, "browse": true, "get": true, "show": true, "download": true,
	"clone": true, "checkout": true, "search": true,
}

var ghWriteVerbs = map[string]bool{
	"create": true, "delete": true, "edit": true, "merge": true, "close": true,
	"reopen": true, "comment": true, "rename": true, "fork": true, "sync": true,
	"archive": true, "unarchive": true, "transfer": true, "add": true,
	"remove": true, "set": true, "run": true, "cancel": true, "rerun": true,
	"disable": true, "enable": true, "lock": true, "unlock": true, "pin": true,
	"unpin": true, "ready": true, "restore": true, "deploy": true,
	"upload": true, "import": true, "review": true, "approve": true, "update": true,
}

// ghReadTopLevel are single-token gh commands that are read-only.
var ghReadTopLevel = map[string]bool{
	"status": true, "browse": true, "search": true,
}

// Classify inspects a gh argument vector and decides its access level.
func (ghProvider) Classify(args []string) Access {
	if len(args) == 0 {
		return AccessUnknown
	}
	cmd := args[0]

	// `gh api` access depends on the HTTP method (default GET; -f/--field imply POST).
	if cmd == "api" {
		method := "GET"
		explicit := false
		for i := 1; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "-X" || a == "--method":
				if i+1 < len(args) {
					method = strings.ToUpper(args[i+1])
					explicit = true
				}
			case strings.HasPrefix(a, "--method="):
				method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
				explicit = true
			case strings.HasPrefix(a, "-X") && len(a) > 2:
				method = strings.ToUpper(a[2:])
				explicit = true
			case a == "-f" || a == "--field" || a == "-F" || a == "--raw-field" || a == "--input":
				if !explicit {
					method = "POST"
				}
			}
		}
		if method == "GET" || method == "HEAD" {
			return AccessRead
		}
		return AccessWrite
	}

	if ghReadTopLevel[cmd] {
		return AccessRead
	}

	// gh <noun> <verb>: the verb is the second token.
	if len(args) >= 2 {
		verb := args[1]
		if ghReadVerbs[verb] {
			return AccessRead
		}
		if ghWriteVerbs[verb] {
			return AccessWrite
		}
	}
	return AccessUnknown
}

// EnumerateContexts scans gh's hosts.yml for configured GitHub hosts.
func (p ghProvider) EnumerateContexts() ([]AccountContext, error) {
	dir := ghConfigDir()
	if dir == "" {
		return nil, nil
	}
	return p.enumerateContextsFromDir(dir)
}

// enumerateContextsFromDir parses hosts from a specific gh config directory.
func (ghProvider) enumerateContextsFromDir(dir string) ([]AccountContext, error) {
	f, err := os.Open(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []AccountContext
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// hosts.yml top-level keys (column 0, no indentation) are hostnames.
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		host := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
		if host == "" || strings.Contains(host, " ") {
			continue
		}
		source := "github"
		if host != "github.com" {
			source = "github-enterprise"
		}
		out = append(out, AccountContext{ID: host, Label: host, Source: source})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ContextArgs is unused by gh (it targets hosts via the GH_HOST env var).
func (ghProvider) ContextArgs(c AccountContext) []string { return nil }

// ContextEnv targets a GitHub host via GH_HOST.
func (ghProvider) ContextEnv(c AccountContext) []string {
	if c.ID != "" {
		return []string{"GH_HOST=" + c.ID}
	}
	return nil
}

// ghConfigDir resolves gh's config dir, honoring GH_CONFIG_DIR.
func ghConfigDir() string {
	if d := os.Getenv("GH_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gh")
}

// askGHCommand is the registered cli.Command for the GitHub assistant.
var askGHCommand = newProviderCommand(ghProvider{})

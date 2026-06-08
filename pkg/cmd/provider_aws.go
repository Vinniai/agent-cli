package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// awsProvider is the first Provider implementation, backed by the `aws` CLI.
type awsProvider struct{}

func (awsProvider) Name() string   { return "aws" }
func (awsProvider) Binary() string { return "aws" }

func (awsProvider) SystemPrompt(ctxs []AccountContext) string {
	var b strings.Builder
	b.WriteString("You are an assistant that translates natural-language requests into aws CLI commands.\n")
	b.WriteString("Use the run_command tool to execute commands. Pass the argv WITHOUT the leading 'aws'.\n")
	if len(ctxs) > 0 {
		b.WriteString("\nAvailable AWS profiles (accounts):\n")
		for _, c := range ctxs {
			label := c.Label
			if label == "" {
				label = c.ID
			}
			b.WriteString(fmt.Sprintf("- %s (%s)\n", label, c.Source))
		}
		b.WriteString("\nAccount selection: infer the target from the request and set the run_command 'profiles' field to the matching profile id(s). " +
			"If the user names an environment/account (e.g. \"prod\", \"staging\"), pick the profile whose name matches. " +
			"If they say \"all accounts\"/\"every account\", include every profile above. " +
			"If they don't mention an account, omit 'profiles' to use the default credentials.\n")
	}
	return b.String()
}

// awsReadVerbs / awsWriteVerbs drive Classify. Read verbs are safe to run
// without confirmation; write verbs mutate and require the gate.
var awsReadVerbs = map[string]bool{
	"describe": true, "list": true, "get": true, "ls": true, "search": true,
	"lookup": true, "scan": true, "query": true, "estimate": true, "head": true,
	"show": true, "batch-get": true, "select": true, "preview": true,
	"validate": true, "test": true, "wait": true, "help": true, "view": true,
	"export": true,
}

var awsWriteVerbs = map[string]bool{
	"create": true, "delete": true, "put": true, "update": true, "modify": true,
	"terminate": true, "run": true, "start": true, "stop": true, "reboot": true,
	"attach": true, "detach": true, "associate": true, "disassociate": true,
	"enable": true, "disable": true, "register": true, "deregister": true,
	"add": true, "remove": true, "set": true, "tag": true, "untag": true,
	"authorize": true, "revoke": true, "copy": true, "restore": true,
	"reset": true, "cancel": true, "send": true, "publish": true, "invoke": true,
	"deploy": true, "apply": true, "import": true, "upload": true, "sync": true,
}

// awsS3ReadVerbs / awsS3WriteVerbs handle the `aws s3` high-level command set,
// whose subcommands are not verb-noun (ls, cp, mv, rm, sync, mb, rb, presign).
var awsS3ReadVerbs = map[string]bool{
	"ls": true, "presign": true,
}

var awsS3WriteVerbs = map[string]bool{
	"cp": true, "mv": true, "rm": true, "sync": true, "mb": true, "rb": true,
}

// Classify inspects an aws argument vector and decides its access level.
// AWS operations are `aws <service> <verb-noun>`. We skip the service token,
// take the operation, split it on '-', and match the leading verb.
func (awsProvider) Classify(args []string) Access {
	if len(args) < 2 {
		return AccessUnknown
	}
	service := args[0]
	op := args[1]

	if service == "s3" {
		if awsS3ReadVerbs[op] {
			return AccessRead
		}
		if awsS3WriteVerbs[op] {
			return AccessWrite
		}
		return AccessUnknown
	}

	verb := op
	if i := strings.Index(op, "-"); i >= 0 {
		verb = op[:i]
	}
	if awsReadVerbs[verb] {
		return AccessRead
	}
	if awsWriteVerbs[verb] {
		return AccessWrite
	}
	return AccessUnknown
}

// EnumerateContexts scans the default AWS config/credentials files.
func (p awsProvider) EnumerateContexts() ([]AccountContext, error) {
	dir := awsConfigDir()
	if dir == "" {
		return nil, nil
	}
	return p.enumerateContextsFromDir(dir)
}

// enumerateContextsFromDir parses profiles from a specific directory's
// config and credentials files, deduping by ID.
func (awsProvider) enumerateContextsFromDir(dir string) ([]AccountContext, error) {
	var all []AccountContext

	for _, name := range []string{"config", "credentials"} {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		all = append(all, parseAWSConfig(f)...)
		f.Close()
	}

	// Dedupe by ID, preferring the first occurrence (config before credentials).
	seen := map[string]bool{}
	var out []AccountContext
	for _, c := range all {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out, nil
}

// parseAWSConfig scans an INI-ish reader for AWS section headers and the keys
// within each section. Section headers look like `[profile NAME]`, `[default]`,
// or (in credentials) `[NAME]`. SSO profiles carry sso_* keys -> Source "sso".
func parseAWSConfig(r io.Reader) []AccountContext {
	scanner := bufio.NewScanner(r)

	var contexts []AccountContext
	cur := -1 // index into contexts of the current section, or -1 for none.

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			header := strings.TrimSpace(line[1 : len(line)-1])
			var id, label string
			if header == "default" {
				id = ""
				label = "default"
			} else if strings.HasPrefix(header, "profile ") {
				name := strings.TrimSpace(strings.TrimPrefix(header, "profile "))
				id = name
				label = name
			} else {
				// Bare [NAME] (credentials file).
				id = header
				label = header
				if header == "default" {
					id = ""
				}
			}
			contexts = append(contexts, AccountContext{ID: id, Label: label, Source: "profile"})
			cur = len(contexts) - 1
			continue
		}
		if cur >= 0 {
			key := line
			if i := strings.IndexAny(line, "="); i >= 0 {
				key = strings.TrimSpace(line[:i])
			}
			if strings.HasPrefix(key, "sso_") {
				contexts[cur].Source = "sso"
			}
		}
	}
	return contexts
}

// ContextArgs maps a context to CLI args. AWS targets accounts via the
// --profile flag.
func (awsProvider) ContextArgs(c AccountContext) []string {
	if c.ID != "" {
		return []string{"--profile", c.ID}
	}
	return nil
}

// ContextEnv is unused by AWS (it targets accounts via --profile args).
func (awsProvider) ContextEnv(c AccountContext) []string { return nil }

// DefaultContext targets the profile named by AWS_PROFILE / AWS_DEFAULT_PROFILE
// so `ask aws "..."` works without an explicit --profile when one is exported.
func (awsProvider) DefaultContext() (AccountContext, bool) {
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return AccountContext{ID: v, Label: v, Source: k}, true
		}
	}
	return AccountContext{}, false
}

// awsConfigDir resolves ~/.aws (kept for the production EnumerateContexts path).
func awsConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws")
}

// askAWSCommand is the registered cli.Command for the AWS assistant.
var askAWSCommand = newProviderCommand(awsProvider{})

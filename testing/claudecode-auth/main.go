// claudecode-auth syncs the existing Claude Code OAuth token (stored in the
// macOS keychain item "Claude Code-credentials", or ~/.claude/.credentials.json)
// into an `ask` user_oauth profile named "claude-code", so `ask` can reuse your
// device login instead of an API key:
//
//	go run ./testing/claudecode-auth          # writes the profile
//	./ask --profile claude-code aws "list my S3 buckets"
//
// It writes the token as a STATIC credential (no client_id => the SDK won't try
// to refresh it against the wrong OAuth client). Claude Code keeps the keychain
// token fresh as you use it; re-run this importer (or the `ask-claude` wrapper)
// to pick up the latest token. macOS only (uses `security`).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/config"
)

type ccOAuth struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"` // epoch millis
	Scopes       []string `json:"scopes"`
}

func readKeychain() (*ccOAuth, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err == nil {
		return parse(out)
	}
	// fallback: ~/.claude/.credentials.json
	home, _ := os.UserHomeDir()
	b, ferr := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if ferr != nil {
		return nil, fmt.Errorf("keychain read failed (%v) and no ~/.claude/.credentials.json (%v)", err, ferr)
	}
	return parse(b)
}

func parse(b []byte) (*ccOAuth, error) {
	var wrap struct {
		ClaudeAiOAuth ccOAuth `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	if wrap.ClaudeAiOAuth.AccessToken == "" {
		return nil, fmt.Errorf("no claudeAiOauth.accessToken found (is Claude Code logged in?)")
	}
	return &wrap.ClaudeAiOAuth, nil
}

func main() {
	const profile = "claude-code"
	dir := os.Getenv("ANTHROPIC_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "anthropic")
	}

	cc, err := readKeychain()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Claude Code's public OAuth client id — recorded so the SDK refreshes
	// against the client that minted the token (and to avoid the CLI's
	// "missing client_id, defaulting to ask-cli" notice). Best-effort: if
	// refresh ever fails, just re-run this importer to pull a fresh token.
	const claudeCodeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	cfg := &config.Config{
		AuthenticationInfo: &config.AuthenticationInfo{
			Type: config.AuthenticationTypeUserOAuth,
			UserOAuth: &config.UserOAuth{
				ClientID: claudeCodeClientID,
				Scope:    strings.Join(cc.Scopes, " "),
			},
		},
	}
	if err := config.SaveProfile(dir, profile, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "save profile:", err)
		os.Exit(1)
	}
	exp := time.UnixMilli(cc.ExpiresAt)
	if err := config.WriteCredentials(config.ProfileCredentialsPath(dir, profile), config.Credentials{
		AccessToken:  cc.AccessToken,
		RefreshToken: cc.RefreshToken,
		ExpiresAt:    &exp,
		Scope:        strings.Join(cc.Scopes, " "),
	}); err != nil {
		fmt.Fprintln(os.Stderr, "write credentials:", err)
		os.Exit(1)
	}
	if err := config.SetActiveProfile(dir, profile); err != nil {
		fmt.Fprintln(os.Stderr, "set active profile:", err)
		os.Exit(1)
	}
	fmt.Printf("synced Claude Code token -> ask profile %q (active, expires %s)\nNow just run: ./ask aws \"...\"  (no API key)\n",
		profile, exp.Format(time.Kitchen))
}

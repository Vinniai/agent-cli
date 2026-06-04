package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGHClassify(t *testing.T) {
	p := ghProvider{}
	cases := []struct {
		args []string
		want Access
	}{
		{[]string{"repo", "list"}, AccessRead},
		{[]string{"pr", "view", "123"}, AccessRead},
		{[]string{"issue", "list"}, AccessRead},
		{[]string{"run", "list"}, AccessRead},
		{[]string{"repo", "clone", "o/r"}, AccessRead},
		{[]string{"search", "repos", "cli"}, AccessRead},
		{[]string{"status"}, AccessRead},
		{[]string{"repo", "create", "x"}, AccessWrite},
		{[]string{"repo", "delete", "o/r"}, AccessWrite},
		{[]string{"pr", "merge", "123"}, AccessWrite},
		{[]string{"issue", "close", "123"}, AccessWrite},
		{[]string{"pr", "review", "123"}, AccessWrite},
		{[]string{"api", "/user"}, AccessRead},
		{[]string{"api", "user/repos"}, AccessRead},
		{[]string{"api", "-X", "POST", "/repos/o/r/issues"}, AccessWrite},
		{[]string{"api", "repos/o/r", "--method", "DELETE"}, AccessWrite},
		{[]string{"api", "/repos/o/r/labels", "-f", "name=bug"}, AccessWrite},
		{[]string{"frobnicate", "wat"}, AccessUnknown},
		{[]string{}, AccessUnknown},
	}
	for _, c := range cases {
		if got := p.Classify(c.args); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestGHEnumerateContexts(t *testing.T) {
	dir := t.TempDir()
	hosts := "" +
		"github.com:\n" +
		"    user: alice\n" +
		"    oauth_token: gho_x\n" +
		"    git_protocol: https\n" +
		"enterprise.acme.com:\n" +
		"    user: bob\n" +
		"    oauth_token: gho_y\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ghProvider{}.enumerateContextsFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	if !ids["github.com"] || !ids["enterprise.acme.com"] {
		t.Fatalf("expected both hosts, got %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hosts, got %d: %v", len(got), got)
	}
}

func TestGHContextEnvAndArgs(t *testing.T) {
	p := ghProvider{}
	if got := p.ContextEnv(AccountContext{ID: "enterprise.acme.com"}); !reflect.DeepEqual(got, []string{"GH_HOST=enterprise.acme.com"}) {
		t.Fatalf("ContextEnv = %v, want [GH_HOST=enterprise.acme.com]", got)
	}
	if got := p.ContextEnv(AccountContext{ID: ""}); got != nil {
		t.Fatalf("ContextEnv for empty id = %v, want nil", got)
	}
	if got := p.ContextArgs(AccountContext{ID: "github.com"}); got != nil {
		t.Fatalf("ContextArgs = %v, want nil (gh targets via env)", got)
	}
}

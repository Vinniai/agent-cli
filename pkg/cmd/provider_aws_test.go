package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestAWSClassify(t *testing.T) {
	p := awsProvider{}
	cases := []struct {
		name string
		args []string
		want Access
	}{
		{"s3api list-buckets", []string{"s3api", "list-buckets"}, AccessRead},
		{"ec2 describe-instances", []string{"ec2", "describe-instances"}, AccessRead},
		{"s3api create-bucket", []string{"s3api", "create-bucket", "--bucket", "x"}, AccessWrite},
		{"ec2 terminate-instances", []string{"ec2", "terminate-instances", "--instance-ids", "i-1"}, AccessWrite},
		{"s3 ls", []string{"s3", "ls"}, AccessRead},
		{"s3 rm", []string{"s3", "rm", "s3://b/k"}, AccessWrite},
		{"unknown service+verb", []string{"frobnicate", "wat"}, AccessUnknown},
		{"empty", []string{}, AccessUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Classify(tc.args)
			if got != tc.want {
				t.Fatalf("Classify(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestAWSEnumerateContexts(t *testing.T) {
	dir := t.TempDir()
	configBody := `[default]
region = us-east-1

[profile prod]
region = us-west-2

[profile sso-dev]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1
`
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := awsProvider{}.enumerateContextsFromDir(dir)
	if err != nil {
		t.Fatalf("enumerateContextsFromDir: %v", err)
	}

	// Build a comparable view keyed by ID.
	type view struct {
		ID, Label, Source string
	}
	var views []view
	for _, c := range got {
		views = append(views, view{c.ID, c.Label, c.Source})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })

	want := []view{
		{"", "default", "profile"},    // default profile: empty ID
		{"prod", "prod", "profile"},   // ordinary profile
		{"sso-dev", "sso-dev", "sso"}, // sso_* keys -> source "sso"
	}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })

	if !reflect.DeepEqual(views, want) {
		t.Fatalf("enumerateContextsFromDir =\n  %#v\nwant\n  %#v", views, want)
	}
}

func TestAWSContextArgs(t *testing.T) {
	p := awsProvider{}

	if got := p.ContextArgs(AccountContext{ID: "prod", Label: "prod", Source: "profile"}); !reflect.DeepEqual(got, []string{"--profile", "prod"}) {
		t.Fatalf("ContextArgs(prod) = %#v, want [--profile prod]", got)
	}

	if got := p.ContextArgs(AccountContext{ID: "", Label: "default", Source: "profile"}); got != nil {
		t.Fatalf("ContextArgs(default) = %#v, want nil", got)
	}
}

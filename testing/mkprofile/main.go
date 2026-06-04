// Throwaway helper: writes a FAKE user_oauth profile (non-expiring, bogus token)
// into a config dir so we can prove the OAuth credential path flows through
// `ask <provider>` without a real browser login. Not committed / dev-only.
//
//	go run ./testing/mkprofile /tmp/askoauth
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go/config"
)

func main() {
	dir := "/tmp/askoauth"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	const profile = "demo-oauth"
	exp := time.Now().Add(365 * 24 * time.Hour) // far future -> SDK won't refresh

	cfg := &config.Config{
		AuthenticationInfo: &config.AuthenticationInfo{
			Type: config.AuthenticationTypeUserOAuth,
			UserOAuth: &config.UserOAuth{
				ClientID: "41077d10-94b8-4194-be48-d251e9eb21b4",
				Scope:    "user:profile user:inference user:developer",
			},
		},
	}
	if err := config.SaveProfile(dir, profile, cfg); err != nil {
		panic(err)
	}
	if err := config.WriteCredentials(config.ProfileCredentialsPath(dir, profile), config.Credentials{
		AccessToken:  "fake-oauth-access-token-DEMO",
		RefreshToken: "fake-refresh-token-DEMO",
		ExpiresAt:    &exp,
		Scope:        "user:profile user:inference user:developer",
	}); err != nil {
		panic(err)
	}
	if err := config.SetActiveProfile(dir, profile); err != nil {
		panic(err)
	}
	fmt.Printf("wrote user_oauth profile %q (active) in %s\n", profile, dir)
}

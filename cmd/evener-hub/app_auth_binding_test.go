package hub

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
)

func TestAuth_DevicePollRejectsAFlowForAnotherCodexInstance(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providers := writeProvidersToml(t, dir, "[providers.alpha]\nbase = \"openai-codex\"\n[providers.beta]\nbase = \"openai-codex\"\n")
	c := newTestAuthController(t, dir, stateDir, providers)
	c.generateState = func() (string, error) { return "flow-alpha", nil }
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	pollCalls := 0
	c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		pollCalls++
		if pollCalls == 1 {
			return authopenai.DeviceCodeSuccess{}, true, nil
		}
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "code", CodeVerifier: "verifier"}, false, nil
	}
	c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "alpha"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	_, err = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "beta", FlowID: start.FlowID})
	if err == nil {
		t.Fatal("DevicePoll(beta) succeeded for alpha flow")
	}
	if pollCalls != 0 {
		t.Fatalf("pollDeviceOnce called %d times for mismatched provider", pollCalls)
	}
	if _, loadErr := authopenai.LoadAuth(stateDir, "beta"); !errors.Is(loadErr, authopenai.ErrAuthNotFound) {
		t.Fatalf("beta auth file error = %v, want not found", loadErr)
	}
	pending, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "alpha", FlowID: start.FlowID})
	if err != nil || pending.State != "pending" {
		t.Fatalf("original alpha flow = %+v err=%v, want pending", pending, err)
	}
	authorized, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "alpha", FlowID: start.FlowID})
	if err != nil || authorized.State != "authorized" || authorized.Status == nil || authorized.Status.Provider != "alpha" {
		t.Fatalf("original alpha flow = %+v err=%v, want authorized alpha", authorized, err)
	}
	if _, loadErr := authopenai.LoadAuth(stateDir, "beta"); !errors.Is(loadErr, authopenai.ErrAuthNotFound) {
		t.Fatalf("beta auth file after alpha success error = %v, want not found", loadErr)
	}
}

func TestAuth_NonCodexLogoutReportsActualStoredRemoval(t *testing.T) {
	for _, tc := range []struct {
		name       string
		env        map[string]string
		stored     bool
		wantRemove bool
		wantSource string
	}{
		{name: "absent", env: map[string]string{}, wantSource: "none"},
		{name: "environment only", env: map[string]string{"WORK_ANT_KEY": "env-key"}, wantSource: "env:WORK_ANT_KEY"},
		{name: "stored", env: map[string]string{}, stored: true, wantRemove: true, wantSource: "none"},
		{name: "stored shadowed by environment", env: map[string]string{"WORK_ANT_KEY": "env-key"}, stored: true, wantRemove: true, wantSource: "env:WORK_ANT_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			dir := t.TempDir()
			c := newTestAuthController(t, dir, filepath.Join(dir, "state"), writeProvidersToml(t, dir, bearerInstanceToml), tc.env)
			if tc.stored {
				if err := c.creds.Set("work-ant", "stored-key"); err != nil {
					t.Fatalf("Set: %v", err)
				}
				if err := c.reg.Reload(); err != nil {
					t.Fatalf("Reload: %v", err)
				}
			}
			before, err := c.Status(appwire.AuthStatusParams{Provider: "work-ant"})
			if err != nil {
				t.Fatalf("Status before logout: %v", err)
			}
			resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "work-ant"})
			if err != nil {
				t.Fatalf("Logout: %v", err)
			}
			if resp.Removed != tc.wantRemove {
				t.Fatalf("Removed=%v, want %v (before=%+v)", resp.Removed, tc.wantRemove, before)
			}
			if resp.Status.ActiveSource != tc.wantSource {
				t.Fatalf("source after logout=%q, want %q", resp.Status.ActiveSource, tc.wantSource)
			}
		})
	}
}

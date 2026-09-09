package hub

import (
	"context"
	"errors"
	"net/http"
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
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("DevicePoll(beta) error = %v, want invalid params", err)
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

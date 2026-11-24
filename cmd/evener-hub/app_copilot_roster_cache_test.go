package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/llm/registry"
)

// seedCopilotAccount stores a launchable Copilot record under instance for one
// GitHub account, so the registry fingerprints that account's roster.
func seedCopilotAccount(t *testing.T, stateDir, instance, accountID string) {
	t.Helper()
	record := githubcopilot.AuthRecord{
		Instance: instance, Scheme: githubcopilot.AuthScheme, Version: 1,
		AccessToken: "github-access-" + accountID, CopilotToken: "copilot-session-" + accountID,
		AccountID: accountID, Email: "octocat@example.test",
		ObtainedAt: time.Now().Add(-time.Hour), CopilotExpiry: time.Now().Add(24 * time.Hour),
	}
	if err := githubcopilot.SaveAuth(stateDir, instance, record); err != nil {
		t.Fatalf("SaveAuth(%s): %v", instance, err)
	}
}

// TestCopilotDeviceLoginAccountSwitchDropsTheOldAccountsRoster pins whose rows
// the reload after a hub-run device login may keep: the record is saved before
// the reload fingerprints it, so both sides of the comparison would otherwise
// read the new account. A switch must invalidate the instance's cached roster,
// and a same-account sign-in must leave it alone.
func TestCopilotDeviceLoginAccountSwitchDropsTheOldAccountsRoster(t *testing.T) {
	for _, cfg := range []struct {
		name string
		toml string
	}{
		{name: "configured transport", toml: copilotDeviceInstanceToml},
		{name: "gateway generation", toml: gatewayCopilotDeviceToml},
	} {
		for _, tt := range []struct {
			name           string
			oldAccount     string
			newAccount     string
			wantLiveRemain int
		}{
			{name: "account switch", oldAccount: "1", newAccount: "42", wantLiveRemain: 0},
			{name: "same account renewal", oldAccount: "42", newAccount: "42", wantLiveRemain: 1},
		} {
			t.Run(cfg.name+"/"+tt.name, func(t *testing.T) {
				ctrl, transport, now := newCopilotDeviceTestController(t, cfg.toml)
				seedCopilotAccount(t, ctrl.stateDir, "work", tt.oldAccount)
				if err := ctrl.reloadRegistry(); err != nil {
					t.Fatalf("reload the seeded registry: %v", err)
				}
				ctrl.registry().ApplyLiveListing("work", registry.LiveListing{
					Rows: []registry.Model{{ID: "old-account-roster"}}, Authoritative: true,
				})
				// Mirror the server wiring: the device login adopts its own record
				// as already observed, so the later notice sweep stays quiet.
				instances := &hubInstancesController{reg: ctrl.reg, providersConfigPath: ctrl.providersConfigPath, auth: ctrl}
				ctrl.copilotRecordAdopted = instances.adoptCopilotAuthSignature

				transport.user = `{"id":` + tt.newAccount + `,"login":"octocat","email":"octocat@example.test"}`
				transport.polls = []string{`{"access_token":"github-access-token","token_type":"bearer","scope":"read:user"}`}
				flowID := startCopilotDeviceFlow(t, ctrl, "work")
				*now = now.Add(5 * time.Second)
				resp, err := ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: flowID})
				if err != nil {
					t.Fatalf("DevicePoll: %v", err)
				}
				if resp.State != "authorized" {
					t.Fatalf("DevicePoll state = %q, want authorized", resp.State)
				}
				record, err := githubcopilot.LoadAuth(ctrl.stateDir, "work")
				if err != nil {
					t.Fatalf("LoadAuth(work): %v", err)
				}
				if record.AccountID != tt.newAccount {
					t.Fatalf("saved account = %q, want %q", record.AccountID, tt.newAccount)
				}
				if got := ctrl.registry().LiveModels("work"); len(got) != tt.wantLiveRemain {
					t.Fatalf("cached roster rows = %d, want %d", len(got), tt.wantLiveRemain)
				}
			})
		}
	}
}

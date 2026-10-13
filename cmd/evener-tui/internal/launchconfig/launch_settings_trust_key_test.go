package launchconfig

import (
	"encoding/json"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
)

var trustKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")}

// TestLaunchSettingsTrustKeyTrustsAwaitingRepoFile pins the In-Repo tab's
// advertised T binding: pressing T on an untrusted or changed launch file sends
// the trust request for that file's hash, and the panel adopts the resolved
// config the hub answers with, after which T has nothing left to trust.
func TestLaunchSettingsTrustKeyTrustsAwaitingRepoFile(t *testing.T) {
	const (
		cwd  = "/repo"
		hash = "sha256:abc"
	)
	for _, trust := range []string{"untrusted", "changed"} {
		t.Run(trust, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())

			p := NewLaunchSettingsPanel(client, cwd)
			p.tab = launchTabRepo
			p.resolved.Repo = &appwire.RepoLaunchConfigStatus{Path: cwd + "/.evener/launch.toml", Trust: trust, Hash: hash}

			updated, cmd := p.Update(trustKey)
			if cmd == nil {
				t.Fatal("T on an awaiting-trust repo file issued no command")
			}

			sent := make(chan appwire.Message, 1)
			go func() {
				req := <-transport.Sent()
				sent <- req
				transport.DeliverResponse(req.Request.ID, appwire.LaunchConfigResolved{
					Repo: &appwire.RepoLaunchConfigStatus{Path: cwd + "/.evener/launch.toml", Trust: "trusted", Hash: hash},
				})
			}()
			msg := cmd()

			select {
			case req := <-sent:
				if req.Request.Method != appwire.MethodEvenerLaunchTrustRepo {
					t.Fatalf("sent method %q, want %q", req.Request.Method, appwire.MethodEvenerLaunchTrustRepo)
				}
				var params appwire.LaunchConfigTrustRepoParams
				if err := json.Unmarshal(req.Request.Params, &params); err != nil {
					t.Fatalf("decode params: %v", err)
				}
				if params != (appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash}) {
					t.Fatalf("trust params = %+v, want cwd %q hash %q", params, cwd, hash)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("T sent no trust request")
			}

			result, ok := msg.(LaunchTrustResultMsg)
			if !ok {
				t.Fatalf("trust command produced %T, want LaunchTrustResultMsg", msg)
			}
			if result.Err != nil {
				t.Fatalf("trust request failed: %v", result.Err)
			}

			next, _ := updated.Update(result)
			panel := next.(LaunchSettingsPanel)
			if panel.done {
				t.Fatal("pressing T dismissed the panel")
			}
			if panel.resolved.Repo == nil || repoAwaitingTrust(panel.resolved.Repo) {
				t.Fatalf("panel repo after trust = %+v, want trusted", panel.resolved.Repo)
			}
			if _, again := panel.Update(trustKey); again != nil {
				t.Fatal("T on an already-trusted repo file issued another command")
			}
		})
	}
}

// TestLaunchSettingsTrustKeyIgnoresPastedT keeps trust a deliberate keypress:
// a bracketed paste whose text is "T" is not the trust key.
func TestLaunchSettingsTrustKeyIgnoresPastedT(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/repo")
	p.tab = launchTabRepo
	p.resolved.Repo = &appwire.RepoLaunchConfigStatus{Trust: "untrusted", Hash: "h"}
	if _, cmd := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T"), Paste: true}); cmd != nil {
		t.Fatal("pasted T issued a trust command")
	}
}

// TestLaunchSettingsTrustKeyIgnoredOutsideAwaitingRepoFile keeps T inert where
// there is nothing to trust: a trusted file, a missing file, or another tab.
func TestLaunchSettingsTrustKeyIgnoredOutsideAwaitingRepoFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  launchTab
		repo *appwire.RepoLaunchConfigStatus
	}{
		{"trusted file", launchTabRepo, &appwire.RepoLaunchConfigStatus{Trust: "trusted", Hash: "h"}},
		{"no file", launchTabRepo, nil},
		{"untrusted without hash", launchTabRepo, &appwire.RepoLaunchConfigStatus{Trust: "untrusted"}},
		{"global tab", launchTabGlobal, &appwire.RepoLaunchConfigStatus{Trust: "untrusted", Hash: "h"}},
		{"project tab", launchTabProject, &appwire.RepoLaunchConfigStatus{Trust: "untrusted", Hash: "h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewLaunchSettingsPanel(nil, "/repo")
			p.tab = tc.tab
			p.resolved.Repo = tc.repo
			next, cmd := p.Update(trustKey)
			if cmd != nil {
				t.Fatal("T issued a command with nothing to trust")
			}
			if panel := next.(LaunchSettingsPanel); panel.done || panel.tab != tc.tab {
				t.Fatalf("T changed panel state: done=%v tab=%v", panel.done, panel.tab)
			}
		})
	}
}

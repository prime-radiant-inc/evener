package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/internal/appserver"
)

const testCredentialJSON = "{\n  \"type\": \"authorized_user\",\n  \"client_id\": \"a\",\n  \"client_secret\": \"b\",\n  \"refresh_token\": \"c\"\n}"

// credentialJSONHub is a hub that records what evener/auth/credentialJson/set
// received, so a test can prove the TUI sent the paste unaltered.
func credentialJSONHub(t *testing.T) (*appwire.Client, *appwire.AuthCredentialJsonSetParams, func()) {
	t.Helper()
	got := &appwire.AuthCredentialJsonSetParams{}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthCredentialJsonSet, func(_ context.Context, params appwire.AuthCredentialJsonSetParams) (appwire.AuthStatusResponse, error) {
			*got = params
			return appwire.AuthStatusResponse{Provider: params.Provider, SignedIn: true, ActiveSource: "store"}, nil
		})
	})
	return client, got, cleanup
}

// TestCredentialJsonAction_OpensAPasteModal: the panel's credential-JSON
// action opens an input tagged for this instance, so the result routes back
// to the credential-JSON store rather than the API-key one.
func TestCredentialJsonAction_OpensAPasteModal(t *testing.T) {
	client, _, cleanup := credentialJSONHub(t)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	updated, _ := m.handleCredentialsAction(launchconfig.CredentialsActionMsg{Action: "setCredentialJson", Instance: "google-vertex"})
	modal := updated.(hubModel).followupModal
	if modal == nil {
		t.Fatal("the credential-JSON action must open an input modal")
	}
	view := modal.View()
	if !strings.Contains(view, "google-vertex") || !strings.Contains(strings.ToLower(view), "credential json") {
		t.Fatalf("modal view = %q, want it to name the instance and the credential JSON", view)
	}
}

// TestCredentialJsonResult_SendsThePasteAndReadsAPath: the two prompts are
// separate — one takes the document, the other a path to the file holding it,
// which the TUI reads on the machine the user typed it on — so neither has to
// guess which it was given.
func TestCredentialJsonResult_SendsThePasteAndReadsAPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(path, []byte(testCredentialJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, tag, value string
	}{
		{name: "pasted document", tag: "credential-json-set:google-vertex", value: testCredentialJSON},
		{name: "document with surrounding space", tag: "credential-json-set:google-vertex", value: "  " + testCredentialJSON + "  "},
		{name: "path to the file", tag: "credential-json-file:google-vertex", value: path},
		{name: "path with surrounding space", tag: "credential-json-file:google-vertex", value: "  " + path + "  "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, got, cleanup := credentialJSONHub(t)
			defer cleanup()
			m := newHubModel(client, "http://hub.test")
			m.followupModal = &tuipick.TextInputModal{}
			updated, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: tt.tag, Value: tt.value})
			if updated.(hubModel).followupModal != nil {
				t.Fatal("the modal should be dismissed")
			}
			if cmd == nil {
				t.Fatal("a credential-JSON result should produce a cmd")
			}
			res, ok := cmd().(launchconfig.AuthApiKeySetResultMsg)
			if !ok || res.Err != nil || !res.Status.SignedIn {
				t.Fatalf("result = %#v", cmd())
			}
			if got.Provider != "google-vertex" || got.Value != testCredentialJSON {
				t.Fatalf("hub received provider=%q value=%q, want the credential JSON verbatim for google-vertex", got.Provider, got.Value)
			}
		})
	}
}

// TestCredentialJsonFile_ReadsOffTheUpdateLoop: the file is read inside the
// command, not while handling the key, so a slow filesystem cannot hold up
// the interface. The file is created only after the handler returns, so a
// read that happened there would have failed and the command would carry
// that failure instead of the document.
func TestCredentialJsonFile_ReadsOffTheUpdateLoop(t *testing.T) {
	client, got, cleanup := credentialJSONHub(t)
	defer cleanup()
	path := filepath.Join(t.TempDir(), "adc.json")
	m := newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	_, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Value: path})
	if cmd == nil {
		t.Fatal("want a cmd")
	}
	if got.Value != "" {
		t.Fatalf("nothing should have been stored yet; the hub received %q", got.Value)
	}
	if err := os.WriteFile(path, []byte(testCredentialJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, ok := cmd().(launchconfig.AuthApiKeySetResultMsg); !ok || res.Err != nil {
		t.Fatalf("the read belongs in the cmd, which runs after this file exists: %#v", cmd())
	}
	if got.Value != testCredentialJSON {
		t.Fatalf("hub received %q, want the file's contents", got.Value)
	}
}

// TestCredentialJsonFile_UnreadablePathIsReportedAndNotSent: a path that
// cannot be read is the user's mistake to see — by its reason, since the
// value itself is never repeated — and nothing is stored.
func TestCredentialJsonFile_UnreadablePathIsReportedAndNotSent(t *testing.T) {
	client, got, cleanup := credentialJSONHub(t)
	defer cleanup()
	missing := filepath.Join(t.TempDir(), "no-such-credentials.json")
	m := newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	updated, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Value: missing})
	if updated.(hubModel).followupModal != nil {
		t.Fatal("the modal should be dismissed")
	}
	if cmd == nil {
		t.Fatal("want a cmd carrying the failure")
	}
	res, ok := cmd().(launchconfig.AuthApiKeySetResultMsg)
	if !ok || res.Err == nil || !strings.Contains(res.Err.Error(), "no such file or directory") {
		t.Fatalf("result = %#v, want one giving the reason the read failed", cmd())
	}
	if strings.Contains(res.Err.Error(), missing) {
		t.Fatalf("err = %q, must not repeat what was submitted", res.Err)
	}
	if got.Value != "" {
		t.Fatalf("the hub must not be called; it received %q", got.Value)
	}
}

// TestCredentialJsonFile_NeverEchoesWhatWasSubmitted: the error line is
// rendered and persists after the panel closes, so a value that cannot be
// read as a path — a secret typed into the file prompt by mistake — must not
// put any of itself there. The reason it failed is what the user needs.
func TestCredentialJsonFile_NeverEchoesWhatWasSubmitted(t *testing.T) {
	const wrongSecret = `sk-ant-api03-SECRET-MATERIAL-that-is-not-a-path-or-a-document`
	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "wrong secret", value: wrongSecret},
		{name: "secret with a path separator", value: "AQ.Ab8RN6Jz/SECRET+MATERIAL/x"},
		{name: "mistyped path", value: "/tmp/definitely-not-here/SECRET-MATERIAL.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, got, cleanup := credentialJSONHub(t)
			defer cleanup()
			m := newHubModel(client, "http://hub.test")
			m.followupModal = &tuipick.TextInputModal{}
			_, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Value: tt.value})
			if cmd == nil {
				t.Fatal("want a cmd carrying the failure")
			}
			res, ok := cmd().(launchconfig.AuthApiKeySetResultMsg)
			if !ok || res.Err == nil {
				t.Fatalf("the failure must be reported: %#v", cmd())
			}
			err := res.Err
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), tt.value) {
				t.Fatalf("err = %q, must not repeat what was submitted", err)
			}
			if !strings.Contains(err.Error(), "credential JSON") {
				t.Fatalf("err = %q, want it to name what failed", err)
			}
			if got.Value != "" {
				t.Fatalf("the hub must not be called; it received %q", got.Value)
			}
		})
	}
}

// assertCredentialPathRefused runs the credential-JSON result for a path the
// prompt must refuse, on a goroutine with a deadline: the read happens on the
// update loop, so a path that never returns has to be refused before it is
// opened rather than hanging the interface.
func assertCredentialPathRefused(t *testing.T, value, want string) {
	t.Helper()
	client, got, cleanup := credentialJSONHub(t)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	done := make(chan tea.Msg, 1)
	go func() {
		_, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Value: value})
		if cmd == nil {
			t.Error("want a cmd carrying the failure")
			done <- nil
			return
		}
		done <- cmd()
	}()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading blocked: the path was opened instead of being refused")
	}
	res, ok := msg.(launchconfig.AuthApiKeySetResultMsg)
	if !ok || res.Err == nil || !strings.Contains(res.Err.Error(), want) {
		t.Fatalf("result = %#v, want an error saying %q", msg, want)
	}
	if got.Value != "" {
		t.Fatalf("the hub must not be called; it received %q", got.Value)
	}
}

// TestCredentialJsonResult_RefusesWhatIsNotAReadableFile covers the refusals
// every platform can express; the pipe and device cases are in the unix file.
func TestCredentialJsonResult_RefusesWhatIsNotAReadableFile(t *testing.T) {
	dir := t.TempDir()
	huge := filepath.Join(dir, "huge.json")
	if err := os.WriteFile(huge, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(huge, maxCredentialFileBytes+1); err != nil {
		t.Fatal(err)
	}
	t.Run("directory", func(t *testing.T) { assertCredentialPathRefused(t, dir, "not a regular file") })
	t.Run("too large", func(t *testing.T) { assertCredentialPathRefused(t, huge, "too large") })
}

// TestCredentialJsonResult_CancelledOrEmptyStoresNothing mirrors the API-key
// path: a dismissed prompt is not a credential.
func TestCredentialJsonResult_CancelledOrEmptyStoresNothing(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  tuipick.TextInputResultMsg
	}{
		{name: "cancelled paste", msg: tuipick.TextInputResultMsg{Tag: "credential-json-set:google-vertex", Cancelled: true}},
		{name: "empty paste", msg: tuipick.TextInputResultMsg{Tag: "credential-json-set:google-vertex", Value: "   "}},
		{name: "cancelled file", msg: tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Cancelled: true}},
		{name: "empty file", msg: tuipick.TextInputResultMsg{Tag: "credential-json-file:google-vertex", Value: "   "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, got, cleanup := credentialJSONHub(t)
			defer cleanup()
			m := newHubModel(client, "http://hub.test")
			m.followupModal = &tuipick.TextInputModal{}
			updated, cmd := m.handleTextInputResult(tt.msg)
			if updated.(hubModel).followupModal != nil {
				t.Fatal("the modal should be dismissed")
			}
			if cmd != nil {
				t.Fatalf("nothing should be sent; got %+v", cmd())
			}
			if got.Value != "" {
				t.Fatalf("the hub must not be called; it received %q", got.Value)
			}
		})
	}
}

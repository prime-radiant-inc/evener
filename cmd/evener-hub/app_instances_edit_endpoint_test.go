package hub

// Regression tests for the #1136 roborev Medium that the instance-edit RPC
// carried no endpoint precondition: the sheet's own stale-draft check reads a
// listing a concurrent change can outdate, so a name re-pointed between that
// check and the RPC was edited with no server-side verification.
// appwire.InstanceEditParams.ExpectedEndpointFingerprint closes it the way the
// credential writes do - the hub checks the assertion under the instance and
// credential locks, so the check and the write are one step, and refuses a
// moved endpoint with the endpoint-conflict error. An absent assertion keeps the
// pre-fingerprint behaviour for the TUI and older clients.

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// createWork creates the instance these tests edit and returns the endpoint
// fingerprint the form would have been opened on.
func createWork(t *testing.T, f *instancesFixture) string {
	t.Helper()
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://work.example.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	shown := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if shown == "" {
		t.Fatal("fixture drift: the hub must be able to key work's endpoint fingerprint")
	}
	return shown
}

// TestInstances_EditRefusesAMovedEndpoint is the guard: a form opened on one
// endpoint must not edit the instance a concurrent change has put under the same
// name. The re-pointing edit itself carries no assertion (the TUI path), so it
// lands and moves the endpoint; the assertion captured before it must then be
// refused, and nothing the refused edit carried may reach providers.toml.
func TestInstances_EditRefusesAMovedEndpoint(t *testing.T) {
	f := newInstancesFixture(t, nil)
	shown := createWork(t, f)

	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:    "work",
		BaseURL: "https://work.example.test/v2",
	}); err != nil {
		t.Fatalf("the re-pointing edit: %v", err)
	}
	moved := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if moved == shown {
		t.Fatal("fixture drift: re-pointing base_url must move the endpoint fingerprint")
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:                        "work",
		APIKeyEnv:                   "STALE_KEY",
		ExpectedEndpointFingerprint: shown,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("Edit with a stale assertion = %v, want an appwire Conflict", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "save again") {
		t.Fatalf("refusal %q does not tell the user to save again", msg)
	}
	if strings.Contains(msg, "credential") {
		t.Fatalf("refusal %q tells an instance edit to re-enter a credential, which its form does not offer", msg)
	}
	if got := authoredEntry(t, f.tomlPath, "work"); len(got.APIKeyEnv) != 0 {
		t.Fatalf("the refused edit still wrote api_key_env = %v", got.APIKeyEnv)
	}
}

// TestInstances_EditAcceptsTheEndpointTheFormWasOpenedOn is the positive
// control: an assertion that still describes where the name resolves is not a
// refusal, and the edit it carries lands.
func TestInstances_EditAcceptsTheEndpointTheFormWasOpenedOn(t *testing.T) {
	f := newInstancesFixture(t, nil)
	shown := createWork(t, f)

	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:                        "work",
		APIKeyEnv:                   "FRESH_KEY",
		ExpectedEndpointFingerprint: shown,
	}); err != nil {
		t.Fatalf("Edit with the current assertion: %v", err)
	}
	if got := authoredEntry(t, f.tomlPath, "work"); len(got.APIKeyEnv) != 1 || got.APIKeyEnv[0] != "FRESH_KEY" {
		t.Fatalf("api_key_env = %v, want [FRESH_KEY]", got.APIKeyEnv)
	}
}

// TestInstances_EditWithoutAnAssertionKeepsItsBehaviour pins the backward
// compatibility the field promises: the TUI and older clients send no
// assertion, and an edit on a name that moved since they read it still lands,
// exactly as it did before the field existed.
func TestInstances_EditWithoutAnAssertionKeepsItsBehaviour(t *testing.T) {
	f := newInstancesFixture(t, nil)
	createWork(t, f)

	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:    "work",
		BaseURL: "https://work.example.test/v2",
	}); err != nil {
		t.Fatalf("the moving edit: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "TUI_KEY"}); err != nil {
		t.Fatalf("Edit without an assertion: %v", err)
	}
	if got := authoredEntry(t, f.tomlPath, "work"); len(got.APIKeyEnv) != 1 || got.APIKeyEnv[0] != "TUI_KEY" {
		t.Fatalf("api_key_env = %v, want [TUI_KEY]", got.APIKeyEnv)
	}
}

// TestInstances_EditResolvesTheKeyBeforeBothLocks pins the lock threading: the
// assertion's key is resolved before Edit takes mu and credMu, so a key repair
// (an inter-process lock and a write) cannot stall every listing and credential
// op behind the exclusive section - the fix round 59 made for the credential
// writes and Remove.
func TestInstances_EditResolvesTheKeyBeforeBothLocks(t *testing.T) {
	f := newInstancesFixture(t, nil)
	shown := createWork(t, f)

	probe := installKeyResolutionProbe(t,
		namedLock{name: "the instances lock (mu)", lock: &f.ctl.mu},
		namedLock{name: "the credential lock (credMu)", lock: &f.ctl.auth.credMu},
	)
	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:                        "work",
		APIKeyEnv:                   "ORDERED_KEY",
		ExpectedEndpointFingerprint: shown,
	}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	probe.assertResolvedBeforeTheLocks(t, "Edit")
}

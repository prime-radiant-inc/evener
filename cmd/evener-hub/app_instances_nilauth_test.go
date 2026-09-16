package hub

// Regression tests for the nil-auth guard on hubInstancesController
// (roborev Low on PR #1136, head 2ccdde1).
//
// A bare controller - one constructed with no hubAuthController wired, as some
// callers do - must keep describing the registry it holds (List) while refusing
// every change that would read or move credentials. Before this fix entryFor
// dereferenced c.auth unconditionally and each mutator dereferenced c.auth.credMu
// unconditionally, so a bare controller with a loaded registry panicked instead
// of refusing.

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// detachAuth makes f a bare controller - one with no hubAuthController - by
// removing the auth controller after the fixture is built, restoring it when the
// test ends so a shared fixture would be left unchanged.
func detachAuth(t *testing.T, f *instancesFixture) {
	t.Helper()
	auth := f.ctl.auth
	if auth == nil {
		t.Fatal("fixture has no auth controller to detach")
	}
	f.ctl.auth = nil
	t.Cleanup(func() { f.ctl.auth = auth })
}

// TestInstances_BareControllerListsWithoutCredentialStatus pins that List on a
// controller with no auth controller neither panics nor drops the registry rows:
// there is simply no credential layer, so every row's ActiveSource is empty.
func TestInstances_BareControllerListsWithoutCredentialStatus(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	detachAuth(t, f)

	resp := f.ctl.List() // must not panic: entryFor derives no credential status
	got := entry(t, resp, "work")
	if got.Name != "work" {
		t.Fatalf("bare List lost the instance row: %+v", got)
	}
	if got.ActiveSource != "" {
		t.Fatalf("ActiveSource = %q on a bare controller; want empty", got.ActiveSource)
	}
	if got.HasStoredFile || got.HasStoredOAuth {
		t.Fatalf("bare List reported stored credential state: %+v", got)
	}
	if len(resp.Instances) == 0 {
		t.Fatal("bare List returned no instance rows")
	}
}

// TestInstances_BareControllerRefusesChanges pins that each mutator refuses with
// the internal-error refusal naming the missing credential controller instead of
// dereferencing nil. The seeded "work" instance is what carries Remove past its
// existence check to the credential lock the removal holds, so the guard removal
// there is exercised rather than a not-found path. A panic fails the test on its
// own; that is the point.
func TestInstances_BareControllerRefusesChanges(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	detachAuth(t, f)

	cases := []struct {
		name string
		call func() error
	}{
		{"Create", func() error {
			return f.ctl.Create(appwire.InstanceCreateParams{Name: "x", Base: "openai"})
		}},
		{"Edit", func() error {
			return f.ctl.Edit(appwire.InstanceEditParams{Name: "work"})
		}},
		{"Remove", func() error {
			return f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
		}},
		{"SetDefault", func() error {
			return f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s on a bare controller returned nil; want the missing-credential-controller refusal", tc.name)
			}
			if !strings.Contains(err.Error(), "no credential controller") {
				t.Fatalf("%s error = %v; want it to name the missing credential controller", tc.name, err)
			}
		})
	}
}

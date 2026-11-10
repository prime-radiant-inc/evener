package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// requireMutationLocksReleased fails when a finished mutation left the
// controller lock or the credential lock held, which would wedge every later
// listing and credential write.
func requireMutationLocksReleased(t *testing.T, f *instancesFixture, after string) {
	t.Helper()
	if !f.ctl.mu.TryLock() {
		t.Fatalf("%s left the controller lock held", after)
	}
	f.ctl.mu.Unlock()
	if !f.ctl.auth.credMu.TryLock() {
		t.Fatalf("%s left the credential lock held", after)
	}
	f.ctl.auth.credMu.Unlock()
}

// TestInstances_EveryMutationReleasesItsLocksOnEveryExit runs each mutation
// through a refusal that lands under its locks and through a success, and
// checks that neither exit leaves a lock behind.
func TestInstances_EveryMutationReleasesItsLocksOnEveryExit(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	create := appwire.InstanceCreateParams{Name: "work", Base: "openai", BaseURL: "https://gw.example.test/v1", Protocol: "openai-chat", Surface: "generic"}

	steps := []struct {
		name    string
		run     func() error
		wantErr bool
	}{
		{"create", func() error { return f.ctl.Create(create) }, false},
		{"create of a taken name", func() error { return f.ctl.Create(create) }, true},
		{"edit", func() error {
			return f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Protocol: "openai-responses"})
		}, false},
		{"edit of an unknown instance", func() error {
			return f.ctl.Edit(appwire.InstanceEditParams{Name: "nowhere", Protocol: "openai-responses"})
		}, true},
		{"set default", func() error { return f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "groq"}) }, false},
		{"set default of an unknown instance", func() error {
			return f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "nowhere"})
		}, true},
		{"set model disabled of an unknown instance", func() error {
			return f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "nowhere", Model: "m", Disabled: true})
		}, true},
		{"remove of an unknown instance", func() error { return f.ctl.Remove(appwire.InstanceRemoveParams{Name: "nowhere"}) }, true},
		{"remove", func() error { return f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}) }, false},
	}
	for _, s := range steps {
		err := s.run()
		if (err != nil) != s.wantErr {
			t.Fatalf("%s: err = %v, wantErr %v", s.name, err, s.wantErr)
		}
		requireMutationLocksReleased(t, f, s.name)
	}
}

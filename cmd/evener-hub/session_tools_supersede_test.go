package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestSupersededRemoteProxyRuleBoundary pins exactly which bindings a re-enable
// may replace, because getting it wrong would let one Manager displace another.
func TestSupersededRemoteProxyRuleBoundary(t *testing.T) {
	live, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	previous := &remoteSessionOwner{
		hubID:   "hub-a",
		ctx:     live,
		binding: appwire.SessionBinding{Ref: "remote:worker", InstanceID: "instance-1", HubID: "hub-a", BindingID: "old", PreferenceRevision: 4},
	}
	cases := []struct {
		name string
		next appwire.SessionBinding
		want bool
	}{
		{"newer revision, same hub and instance", appwire.SessionBinding{InstanceID: "instance-1", HubID: "hub-a", BindingID: "new", PreferenceRevision: 6}, true},
		{
			// The same revision is a second binding for the same authority, not a
			// re-enable: replacing would let a peer take over a live Manager.
			"same revision", appwire.SessionBinding{InstanceID: "instance-1", HubID: "hub-a", BindingID: "new", PreferenceRevision: 4}, false,
		},
		{
			// An older revision is a stale request arriving late.
			"older revision", appwire.SessionBinding{InstanceID: "instance-1", HubID: "hub-a", BindingID: "new", PreferenceRevision: 2}, false,
		},
		{
			// A different hub is a different authority. The hosting hub rechecks the
			// preference row, but the rule must not be the thing that allows it.
			"other hub", appwire.SessionBinding{InstanceID: "instance-1", HubID: "hub-b", BindingID: "new", PreferenceRevision: 9}, false,
		},
		{
			// A replaced instance is a different session, not a re-enable of this one.
			"other instance", appwire.SessionBinding{InstanceID: "instance-2", HubID: "hub-a", BindingID: "new", PreferenceRevision: 9}, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := supersededRemoteProxy(previous, tc.next); got != tc.want {
				t.Fatalf("superseded(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
	// An ended binding is not "superseded": it is already gone, and the ordinary
	// path installs over it without any replacement rule.
	cancel()
	if supersededRemoteProxy(previous, appwire.SessionBinding{InstanceID: "instance-1", HubID: "hub-a", PreferenceRevision: 99}) {
		t.Fatal("an already-ended binding was treated as superseded")
	}
	if supersededRemoteProxy(nil, appwire.SessionBinding{PreferenceRevision: 99}) {
		t.Fatal("a missing binding was treated as superseded")
	}
}

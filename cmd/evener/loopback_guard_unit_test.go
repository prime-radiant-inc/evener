package main

import (
	"context"
	"errors"
	"testing"
)

func TestGuardsLoopbackOnlyUnlessLiveOptIn(t *testing.T) {
	env := func(live string) func(string) string {
		return func(k string) string {
			if k == "EVENER_LIVE_TESTS" {
				return live
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name       string
		liveConfig string
		live       string
		want       bool
	}{
		{"default run", "", "", true},
		{"EVENER_LIVE_TESTS=1", "", "1", false},
		{"-live-config", "/some/providers.toml", "", false},
		{"EVENER_LIVE_TESTS other value", "", "0", true},
	} {
		if got := guardsLoopbackOnly(tc.liveConfig, env(tc.live)); got != tc.want {
			t.Errorf("%s: guard = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLoopbackGuardRefusesAndRecordsOtherHosts(t *testing.T) {
	g := &loopbackGuard{}
	for _, addr := range []string{"gw.example.com:443", "203.0.113.7:80", "[2001:db8::1]:443"} {
		if _, err := g.dial(context.Background(), "tcp", addr); !errors.Is(err, errNonLoopbackDial) {
			t.Errorf("dial %s: err = %v, want the guard's refusal", addr, err)
		}
	}
	if got := g.attempts(); len(got) != 3 {
		t.Fatalf("recorded %v, want all three refused addresses", got)
	}
	// A loopback address passes the guard (nothing listens on port 1, so the
	// dial itself fails, but not with the guard's error).
	if _, err := g.dial(context.Background(), "tcp", "127.0.0.1:1"); errors.Is(err, errNonLoopbackDial) {
		t.Fatalf("loopback refused: %v", err)
	}
	if len(g.attempts()) != 3 {
		t.Fatal("a loopback dial must not be recorded")
	}
}

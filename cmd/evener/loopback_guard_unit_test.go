package main

import (
	"context"
	"errors"
	"testing"
)

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

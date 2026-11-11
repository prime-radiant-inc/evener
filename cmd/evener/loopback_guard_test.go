package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sync"
)

// loopbackGuard refuses to dial anything but loopback and records each refused
// address, so a default test that would issue a real provider request fails
// the run instead of reaching the network.
type loopbackGuard struct {
	mu      sync.Mutex
	refused []string
}

var errNonLoopbackDial = errors.New("default tests may only reach loopback servers")

func (g *loopbackGuard) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		g.mu.Lock()
		g.refused = append(g.refused, address)
		g.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", errNonLoopbackDial, address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (g *loopbackGuard) attempts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.refused)
}

// guardsLoopbackOnly is the decision to install the guard: default runs reach
// loopback servers only, while the explicit live opt-ins (-live-config, or
// EVENER_LIVE_TESTS=1) may reach real providers.
func guardsLoopbackOnly(liveConfigPath string, getenv func(string) string) bool {
	return liveConfigPath == "" && !liveTestsEnabled(getenv)
}

// install makes every client built on http.DefaultTransport (the llm adapters
// clone it) dial through the guard.
func (g *loopbackGuard) install() {
	http.DefaultTransport.(*http.Transport).DialContext = g.dial
}

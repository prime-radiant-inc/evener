package server

import (
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestDaemonRouterMatchesCatalog keeps appwire.Methods (the source of the
// generated protocol doc) in lockstep with what the daemon actually registers.
// The daemon serves the ScopeDaemon + ScopeBoth methods; a mismatch means the
// catalog/doc drifted from the wire.
func TestDaemonRouterMatchesCatalog(t *testing.T) {
	srv := NewServer(ServerConfig{})
	got := excludeMethods(srv.AppServer().Router().Methods(), appwire.ConnectionMethodNames())
	want := appwire.CatalogMethodNames(appwire.ScopeDaemon)

	if d := diffSets(want, got); d != "" {
		t.Fatalf("daemon router vs appwire catalog mismatch:\n%s\nUpdate appwire/protocol.go (and run `make generate`).", d)
	}
}

func excludeMethods(names, drop []string) []string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var out []string
	for _, n := range names {
		if !skip[n] {
			out = append(out, n)
		}
	}
	return out
}

func diffSets(want, got []string) string {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	var missing, extra []string
	for s := range w {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for s := range g {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	out := ""
	if len(missing) > 0 {
		out += "  cataloged but NOT registered: " + strings.Join(missing, ", ") + "\n"
	}
	if len(extra) > 0 {
		out += "  registered but NOT cataloged: " + strings.Join(extra, ", ") + "\n"
	}
	return out
}

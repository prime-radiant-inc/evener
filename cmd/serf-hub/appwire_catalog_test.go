package main

import (
	"path/filepath"
	"sort"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// TestHubRouterMatchesCatalog keeps appwire.Methods (the source of the
// generated protocol doc) in lockstep with what serf-hub actually registers.
// The hub serves the ScopeHub + ScopeBoth methods. ProvidersConfigPath is set
// so the serf/instance/* handlers register (they no-op without it).
func TestHubRouterMatchesCatalog(t *testing.T) {
	cfg := hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		ProvidersConfigPath: filepath.Join(t.TempDir(), "providers.toml"),
	}
	server := newHubAppServer(cfg, appsource.NewRegistry())
	got := excludeHubMethods(server.Router().Methods(), appwire.ConnectionMethodNames())
	want := appwire.CatalogMethodNames(appwire.ScopeHub)

	miss, extra := setDiff(want, got)
	if len(miss) > 0 || len(extra) > 0 {
		t.Fatalf("hub router vs appwire catalog mismatch:\n  cataloged but NOT registered: %v\n  registered but NOT cataloged: %v\nUpdate appwire/protocol.go (and run `make generate`).", miss, extra)
	}
}

func excludeHubMethods(names, drop []string) []string {
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

func setDiff(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
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
	return missing, extra
}

//go:build serffuzz || plugincov

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"primeradiant.com/serf/internal/plugins"
)

type pluginManagerReplay struct {
	err     error
	empty   bool
	updated []plugins.UpgradedPlugin
}

func (m *pluginManagerReplay) SeedDefaultMarketplaces() (bool, error) { return false, m.err }
func (m *pluginManagerReplay) ListMarketplaces() (plugins.Marketplaces, error) {
	if m.err != nil {
		return nil, m.err
	}
	return plugins.Marketplaces{"local": {Source: plugins.Source{Kind: plugins.SourceDirectory, Path: "/tmp/local"}, InstallLocation: "/tmp/local", LastUpdated: time.Unix(0, 0)}}, nil
}
func (m *pluginManagerReplay) AddMarketplace(context.Context, string, plugins.Source) (plugins.MarketplaceRef, error) {
	return plugins.MarketplaceRef{InstallLocation: "/tmp/market"}, m.err
}
func (m *pluginManagerReplay) RemoveMarketplace(string) error                   { return m.err }
func (m *pluginManagerReplay) RefreshMarketplace(context.Context, string) error { return m.err }
func (m *pluginManagerReplay) Browse(context.Context, string) (plugins.Catalog, error) {
	return plugins.Catalog{Name: "market", Plugins: []plugins.CatalogPlugin{{Name: "plug", Description: "desc"}}, SkippedPlugins: []string{"skip"}}, m.err
}
func (m *pluginManagerReplay) List() ([]plugins.ListItem, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.empty {
		return nil, nil
	}
	return []plugins.ListItem{{Plugin: "plug", Marketplace: "market", Version: "1", Enabled: true, AutoUpgrade: true, Broken: true}, {Plugin: "off", Marketplace: "market"}}, nil
}
func (m *pluginManagerReplay) Install(context.Context, string, string) (plugins.InstallEntry, error) {
	return plugins.InstallEntry{Version: "1", InstallPath: "/tmp/plugin", Note: "note"}, m.err
}
func (m *pluginManagerReplay) Remove(string, string) error           { return m.err }
func (m *pluginManagerReplay) SetEnabled(string, string, bool) error { return m.err }
func (m *pluginManagerReplay) UpdateAll(context.Context) ([]plugins.InstallEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.empty {
		return nil, nil
	}
	return []plugins.InstallEntry{{Version: "2"}}, nil
}
func (m *pluginManagerReplay) Upgrade(context.Context, string, string) (plugins.InstallEntry, error) {
	return plugins.InstallEntry{Version: "2"}, m.err
}
func (m *pluginManagerReplay) SetAutoUpgrade(string, string, bool) error { return m.err }
func (m *pluginManagerReplay) Gc() ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.empty {
		return nil, nil
	}
	return []string{"/tmp/cache"}, nil
}
func (m *pluginManagerReplay) Doctor() ([]plugins.DoctorFinding, error) {
	return []plugins.DoctorFinding{{Level: plugins.LevelOK, Message: "healthy"}}, m.err
}
func (m *pluginManagerReplay) UpdateAutoUpgrade(context.Context) ([]plugins.UpgradedPlugin, error) {
	return m.updated, m.err
}

type pluginFailWriter struct{}

func (pluginFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func FuzzPluginCommandSeedCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		var out, errOut bytes.Buffer
		m := &pluginManagerReplay{}
		old := newPluginManager
		oldParse := parsePluginMarketplaceSource
		_ = old()
		newPluginManager = func() pluginManager { return m }
		t.Cleanup(func() {
			newPluginManager = old
			parsePluginMarketplaceSource = oldParse
		})
		run := func(args ...string) { out.Reset(); errOut.Reset(); _ = runPlugin(args, nil, &out, &errOut) }

		run()
		for _, command := range []string{"help", "-h", "--help", "bogus"} {
			run(command)
		}
		for _, command := range [][]string{
			{"marketplace"}, {"marketplace", "wat"},
			{"marketplace", "list"}, {"marketplace", "list", "--json"}, {"marketplace", "list", "--bad"},
			{"marketplace", "add"}, {"marketplace", "add", "owner/repo"}, {"marketplace", "add", "--yes", "owner/repo"}, {"marketplace", "add", "--bad"},
			{"marketplace", "remove"}, {"marketplace", "remove", "market"}, {"marketplace", "remove", "--bad"},
			{"marketplace", "refresh"}, {"marketplace", "refresh", "market"}, {"marketplace", "refresh", "--bad"},
			{"marketplace", "browse"}, {"marketplace", "browse", "market"}, {"marketplace", "browse", "--json", "market"}, {"marketplace", "browse", "--bad"},
		} {
			run(command...)
		}
		parsePluginMarketplaceSource = func(string) (plugins.Source, error) {
			return plugins.Source{}, errors.New("parse")
		}
		run("marketplace", "add", "source")
		parsePluginMarketplaceSource = oldParse

		for _, command := range [][]string{
			{"list"}, {"list", "--json"}, {"list", "--bad"},
			{"install"}, {"install", "bad"}, {"install", "plug@market"}, {"install", "--yes", "plug@market"}, {"install", "--bad"},
			{"remove"}, {"remove", "bad"}, {"remove", "plug@market"}, {"remove", "--bad"},
			{"enable"}, {"enable", "bad"}, {"enable", "plug@market"}, {"enable", "--bad"},
			{"disable"}, {"disable", "bad"}, {"disable", "plug@market"}, {"disable", "--bad"},
			{"upgrade"}, {"upgrade", "bad"}, {"upgrade", "plug@market"}, {"upgrade", "--all"}, {"upgrade", "--bad"},
			{"auto-upgrade"}, {"auto-upgrade", "bad"}, {"auto-upgrade", "plug@market"}, {"auto-upgrade", "--off", "plug@market"}, {"auto-upgrade", "--bad"},
			{"gc"}, {"gc", "--json"}, {"gc", "--bad"},
			{"doctor"}, {"doctor", "--json"}, {"doctor", "--bad"},
			{"check-now"}, {"check-now", "--json"}, {"check-now", "--bad"},
		} {
			run(command...)
		}
		_ = runPluginLifecycle("wat", nil, nil, &out, &errOut)

		m.empty = true
		for _, command := range [][]string{{"list"}, {"upgrade", "--all"}, {"gc"}} {
			run(command...)
		}
		m.empty = false
		m.updated = []plugins.UpgradedPlugin{{Plugin: "plug", Marketplace: "market", Entry: plugins.InstallEntry{Version: "2"}}}
		run("check-now")

		m.err = errors.New("injected")
		for _, command := range [][]string{
			{"marketplace", "list"}, {"marketplace", "add", "--yes", "owner/repo"}, {"marketplace", "remove", "market"}, {"marketplace", "refresh", "market"}, {"marketplace", "browse", "market"},
			{"list"}, {"install", "--yes", "plug@market"}, {"remove", "plug@market"}, {"enable", "plug@market"}, {"disable", "plug@market"}, {"upgrade", "--all"}, {"upgrade", "plug@market"}, {"auto-upgrade", "plug@market"}, {"gc"}, {"doctor"}, {"check-now"}, {"check-now", "--json"},
		} {
			run(command...)
		}

		_ = renderCatalog(&out, plugins.Catalog{Name: "empty"}, false)
		_ = renderCatalog(&out, plugins.Catalog{Name: "empty", SkippedPlugins: []string{"x"}}, false)
		_ = renderCatalog(pluginFailWriter{}, plugins.Catalog{}, true)
		_ = renderMarketplaces(&out, nil, false)
		mk := plugins.Marketplaces{
			"dir":    {Source: plugins.Source{Kind: plugins.SourceDirectory, Path: "p"}},
			"github": {Source: plugins.Source{Kind: plugins.SourceGitHub, Repo: "r"}},
			"url":    {Source: plugins.Source{Kind: plugins.SourceURL, URL: "u"}},
			"sub":    {Source: plugins.Source{Kind: plugins.SourceGitSubdir, URL: "u", Path: "p"}},
			"odd":    {Source: plugins.Source{Kind: plugins.SourceKind("odd")}},
		}
		_ = renderMarketplaces(&out, mk, false)
		_ = renderMarketplaces(pluginFailWriter{}, mk, true)
		_ = renderPluginList(&out, nil, false)
		_ = renderPluginList(pluginFailWriter{}, nil, true)
		_, _ = parseMarketplaceSourceArg("git@example/repo")
		_, _ = parseMarketplaceSourceArg("local")
		_, _, _ = splitPluginRef("a@b@c")
	})
}

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

// writeComponentsPlugin writes a loadable plugin at dir whose loaded
// inventory carries one component of every kind the components read reports —
// one skill, one agent, one command, one MCP server — and two hook events with
// distinct registered-hook counts.
func writeComponentsPlugin(t *testing.T, dir string) {
	t.Helper()
	hubTestWritePlugin(t, dir, "demo")
	writePluginFile(t, dir, filepath.Join("skills", "research", "SKILL.md"),
		"---\nname: research\ndescription: Research a topic.\n---\nResearch it.\n")
	writePluginFile(t, dir, filepath.Join("agents", "helper.md"),
		"---\nname: helper\ndescription: Helps with the task.\n---\nHelping.\n")
	writePluginFile(t, dir, filepath.Join("commands", "build.md"),
		"---\ndescription: Build the project.\n---\nBuild it.\n")
	writePluginFile(t, dir, filepath.Join("hooks", "hooks.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo one"},{"type":"command","command":"echo two"}]}],`+
			`"PostToolUse":[{"hooks":[{"type":"command","command":"echo three"}]}]}}`)
	writePluginFile(t, dir, ".mcp.json", `{"mcpServers":{"lookup":{"command":"lookup-server"}}}`)
}

// writeDiagnosticsPlugin writes a loadable plugin whose load carries every
// diagnostic the components read concatenates: an MCP config layer that fails
// to parse, a command file the loader skips, and two hook events evener never
// fires — one reserved by Claude, one unrecognized.
func writeDiagnosticsPlugin(t *testing.T, dir string) {
	t.Helper()
	hubTestWritePlugin(t, dir, "warny")
	writePluginFile(t, dir, filepath.Join("commands", "bad.md"),
		"---\n: : not valid yaml :\n---\nbody\n")
	writePluginFile(t, dir, filepath.Join("hooks", "hooks.json"),
		`{"hooks":{"Setup":[{"hooks":[{"type":"command","command":"c"}]}],`+
			`"TotallyMadeUp":[{"hooks":[{"type":"command","command":"c"}]}]}}`)
	writePluginFile(t, dir, ".mcp.json", `{"mcpServers":{"broken": `)
}

// writePluginFile writes one file inside a plugin directory, creating parent
// directories as needed.
func writePluginFile(t *testing.T, pluginDir, rel, body string) {
	t.Helper()
	path := filepath.Join(pluginDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// componentsTestController stands up the batch read's world: a controller over
// a temp plugin store whose registry carries two installed entries — demo@acme
// backed by the full fixture, and broken@acme backed by a registered directory
// with no manifest on disk — beside a launch-directory fixture with the same
// shape as demo, and a diagnostics fixture outside the store. It returns the
// controller and the two fixture paths for path-form targets.
func componentsTestController(t *testing.T) (ctl *hubPluginsController, launchDir, diagnosticsDir string) {
	t.Helper()
	root := t.TempDir()
	demoDir := filepath.Join(root, "cache", "demo")
	writeComponentsPlugin(t, demoDir)
	brokenDir := filepath.Join(root, "cache", "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := plugins.Registry{Plugins: map[string][]plugins.InstallEntry{
		"demo@acme":   {{InstallPath: demoDir, Version: "1.0.0", Enabled: true, Source: plugins.Source{Kind: plugins.SourceDirectory, Path: demoDir}}},
		"broken@acme": {{InstallPath: brokenDir, Version: "1.0.0", Enabled: true, Source: plugins.Source{Kind: plugins.SourceDirectory, Path: brokenDir}}},
	}}
	if err := plugins.SaveRegistry(filepath.Join(root, "installed_plugins.json"), registry); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	launchDir = filepath.Join(t.TempDir(), "demo")
	writeComponentsPlugin(t, launchDir)
	diagnosticsDir = filepath.Join(t.TempDir(), "warny")
	writeDiagnosticsPlugin(t, diagnosticsDir)
	ctl = newHubPluginsController(root)
	ctl.mgr.Stderr = io.Discard
	return ctl, launchDir, diagnosticsDir
}

// pluginComponentsServer registers the live plugin handler set, the same
// registration the hub wires, over a test controller.
func pluginComponentsServer(ctl *hubPluginsController) *appserver.Server {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerPluginHandlers(server, ctl)
	return server
}

// dispatchPluginComponents dispatches one evener/plugin/components call
// through the router the live hub registers the method on.
func dispatchPluginComponents(t *testing.T, server *appserver.Server, params appwire.PluginComponentsParams) (appwire.PluginComponentsResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerPluginComponents,
		Params: raw,
	})
	if err != nil {
		return appwire.PluginComponentsResponse{}, err
	}
	response, ok := result.(appwire.PluginComponentsResponse)
	if !ok {
		t.Fatalf("dispatch returned %T, want PluginComponentsResponse", result)
	}
	return response, nil
}

// assertFullComponents pins the loaded inventory of a writeComponentsPlugin
// fixture: the bare (un-namespaced) skill, agent, command and MCP server
// names, and one count row per declared hook event.
func assertFullComponents(t *testing.T, components *appwire.PluginComponents) {
	t.Helper()
	if components == nil {
		t.Fatal("components = nil, want the loaded inventory")
	}
	if !reflect.DeepEqual(components.Skills, []string{"research"}) {
		t.Errorf("skills = %v, want [research]", components.Skills)
	}
	if !reflect.DeepEqual(components.Agents, []string{"helper"}) {
		t.Errorf("agents = %v, want [helper]", components.Agents)
	}
	if !reflect.DeepEqual(components.Commands, []string{"build"}) {
		t.Errorf("commands = %v, want [build]", components.Commands)
	}
	if !reflect.DeepEqual(components.MCPServers, []string{"lookup"}) {
		t.Errorf("mcpServers = %v, want [lookup]", components.MCPServers)
	}
	wantHooks := []appwire.PluginHookEvent{{Event: "PostToolUse", Count: 1}, {Event: "PreToolUse", Count: 2}}
	if !reflect.DeepEqual(components.Hooks, wantHooks) {
		t.Errorf("hooks = %v, want %v", components.Hooks, wantHooks)
	}
	if len(components.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", components.Warnings)
	}
}

// warningsMention reports whether any warning names marker (a file or event
// name the diagnostics fixture declared).
func warningsMention(warnings []string, marker string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, marker) {
			return true
		}
	}
	return false
}

// TestPluginComponents pins the evener/plugin/components handler. One batch
// call answers every target form, echoing the targets in order: an installed
// pair loads through the registry, a path loads the directory directly, and a
// broken or unknown target costs only its own row while the batch keeps its
// other answers. A target carrying both forms or neither is a caller bug that
// fails the whole request as a params error before any load runs.
func TestPluginComponents(t *testing.T) {
	t.Run("batch answers every target form in order", func(t *testing.T) {
		t.Parallel()
		ctl, launchDir, diagnosticsDir := componentsTestController(t)
		server := pluginComponentsServer(ctl)
		targets := []appwire.PluginComponentsTarget{
			{Plugin: "demo", Marketplace: "acme"},
			{Plugin: "broken", Marketplace: "acme"},
			{Path: launchDir},
			{Plugin: "missing", Marketplace: "acme"},
			{Path: diagnosticsDir},
		}

		resp, err := dispatchPluginComponents(t, server, appwire.PluginComponentsParams{Targets: targets})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if len(resp.Results) != len(targets) {
			t.Fatalf("results = %d rows, want one echo per target (%d)", len(resp.Results), len(targets))
		}
		for i, target := range targets {
			if resp.Results[i].PluginComponentsTarget != target {
				t.Errorf("results[%d] echoed %+v, want the requested target %+v", i, resp.Results[i].PluginComponentsTarget, target)
			}
		}

		// The installed pair loads through its registry entry's install path.
		if resp.Results[0].Error != "" {
			t.Fatalf("installed pair error = %q, want none", resp.Results[0].Error)
		}
		assertFullComponents(t, resp.Results[0].Components)

		// A registered directory with no manifest costs only its own row; the
		// batch keeps the other targets' components.
		if resp.Results[1].Components != nil {
			t.Fatalf("broken row components = %+v, want nil", resp.Results[1].Components)
		}
		if resp.Results[1].Error == "" {
			t.Fatal("broken row error = \"\", want the load failure")
		}
		if resp.Results[0].Components == nil || resp.Results[2].Components == nil {
			t.Fatal("a broken target dropped the batch's other answers")
		}

		// A path target loads the launch directory directly, same shape.
		if resp.Results[2].Error != "" {
			t.Fatalf("path target error = %q, want none", resp.Results[2].Error)
		}
		assertFullComponents(t, resp.Results[2].Components)

		// A pair matching nothing costs only its own row; the call succeeded.
		if resp.Results[3].Components != nil || resp.Results[3].Error == "" {
			t.Fatalf("unknown pair row = %+v, want an error-only row", resp.Results[3])
		}

		// A plugin that loads with diagnostics everywhere reports them as
		// warnings beside an otherwise-empty inventory.
		diag := resp.Results[4]
		if diag.Error != "" {
			t.Fatalf("diagnostics row error = %q, want none: the plugin loads", diag.Error)
		}
		if diag.Components == nil {
			t.Fatal("diagnostics row components = nil, want the inventory")
		}
		if len(diag.Components.Skills)+len(diag.Components.Agents)+len(diag.Components.Commands)+
			len(diag.Components.Hooks)+len(diag.Components.MCPServers) != 0 {
			t.Errorf("diagnostics row = %+v, want no loaded components", diag.Components)
		}
		if len(diag.Components.Warnings) != 4 {
			t.Fatalf("warnings = %v, want one per source: the failed MCP layer, the skipped command file, and the two never-firing hook events", diag.Components.Warnings)
		}
		for _, marker := range []string{".mcp.json", "bad.md", "Setup", "TotallyMadeUp"} {
			if !warningsMention(diag.Components.Warnings, marker) {
				t.Errorf("no warning names %s: %v", marker, diag.Components.Warnings)
			}
		}
	})

	t.Run("malformed target fails the whole request", func(t *testing.T) {
		t.Parallel()
		ctl, launchDir, _ := componentsTestController(t)
		server := pluginComponentsServer(ctl)
		// A marketplace without a plugin names nothing: without the refusal it
		// would fall through to the registry miss as an empty-plugin lookup.
		for _, malformed := range []appwire.PluginComponentsTarget{
			{Plugin: "demo", Marketplace: "acme", Path: launchDir}, // both forms
			{},                    // neither
			{Marketplace: "acme"}, // still neither: no plugin, no path
		} {
			targets := []appwire.PluginComponentsTarget{
				{Plugin: "demo", Marketplace: "acme"},
				{Path: launchDir},
				malformed,
			}
			_, err := dispatchPluginComponents(t, server, appwire.PluginComponentsParams{Targets: targets})
			var wireErr appwire.WireError
			if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
				t.Fatalf("malformed target %+v: error = %v, want an invalid-params refusal for the whole request", malformed, err)
			}
		}
	})
}

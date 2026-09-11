package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
)

func TestApplyHubAPILogDefault(t *testing.T) {
	cases := []struct {
		name    string
		layer   launchconfig.Layer
		hubOn   bool
		wantNil bool
		wantOn  bool
		wantHub bool // the floor recorded provenance "hub"
	}{
		{name: "hub off pins false", layer: launchconfig.Layer{}, hubOn: false, wantOn: false, wantHub: true},
		{name: "hub on fills unset", layer: launchconfig.Layer{}, hubOn: true, wantOn: true, wantHub: true},
		{name: "layer true wins over hub on", layer: launchconfig.Layer{APILog: new(true)}, hubOn: true, wantOn: true},
		{name: "layer false wins over hub on", layer: launchconfig.Layer{APILog: new(false)}, hubOn: true, wantOn: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := launchconfig.Resolved{Effective: tc.layer}
			// Provenance starts nil: the floor must allocate it rather than skip
			// recording where a value came from.
			applyHubAPILogDefault(&resolved, tc.hubOn)
			got := resolved.Effective.APILog
			if tc.wantNil {
				if got != nil {
					t.Fatalf("APILog = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("APILog = nil, want a value")
			}
			if *got != tc.wantOn {
				t.Fatalf("APILog = %v, want %v", *got, tc.wantOn)
			}
			if tc.wantHub {
				if got := resolved.Provenance["api_log"]; got != launchconfig.LayerHub {
					t.Fatalf("api_log provenance = %q, want hub", got)
				}
			}
		})
	}
}

// TestHubSpawnerSpawnAPILog pins the hub.toml api_log floor at the real spawn
// boundary: hub api_log=true passes --api-log on only when no launch layer set
// api_log, an explicit launch-layer value wins in both directions, and the
// floor is pinned in both directions — hub off passes --api-log off rather
// than deferring to the child binary's own default, which a pre-change
// evener sets to recording.
func TestHubSpawnerSpawnAPILog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hubOn    bool
		layerAPI *bool
		wantArg  string // "" means the flag must be absent
	}{
		{name: "hub off pins off", hubOn: false, layerAPI: nil, wantArg: "off"},
		{name: "hub on injects on", hubOn: true, layerAPI: nil, wantArg: "on"},
		{name: "launch layer false overrides hub on", hubOn: true, layerAPI: new(false), wantArg: "off"},
		{name: "launch layer true passes on without hub default", hubOn: false, layerAPI: new(true), wantArg: "on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runDir := filepath.Join(dir, "run")
			argsOut := filepath.Join(dir, "args.txt")
			t.Setenv("ARGS_OUT", argsOut)
			bin := filepath.Join(dir, "fake-evener")
			script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
	  printf '{"protocol":"evener-appwire-v5"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
			writeFakeEvener(t, bin, script)

			cfg := DefaultConfig()
			cfg.SpawnTimeout = 2 * time.Second
			cfg.APILog = tc.hubOn
			spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

			if _, err := spawner.Spawn(context.Background(), hubcore.SpawnRequest{
				Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
					Model:  "ollama/test",
					APILog: tc.layerAPI,
				}},
				WorkingDir: dir,
				Provider:   "ollama",
			}); err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			argsData, err := os.ReadFile(argsOut)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			args := strings.Fields(string(argsData))
			got := argValue(args, "--api-log")
			if got != tc.wantArg {
				t.Fatalf("--api-log = %q, want %q\nargs:\n%s", got, tc.wantArg, argsData)
			}
		})
	}
}

// TestHubSpawnerResumeAPILog pins the same floor at the resume boundary:
// buildResumeArgs passes the resolved api_log through to the daemon, so an
// explicit launch-layer value must beat the hub-wide api_log=true, and the
// hub floor must fill a request whose layers left api_log unset, in both
// directions.
func TestHubSpawnerResumeAPILog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hubOn    bool
		layerAPI *bool
		wantArg  string // "" means the flag must be absent
	}{
		{name: "hub off pins off", hubOn: false, layerAPI: nil, wantArg: "off"},
		{name: "hub on injects on", hubOn: true, layerAPI: nil, wantArg: "on"},
		{name: "launch layer false overrides hub on", hubOn: true, layerAPI: new(false), wantArg: "off"},
		{name: "launch layer true passes on without hub default", hubOn: false, layerAPI: new(true), wantArg: "on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runDir := filepath.Join(dir, "run")
			argsOut := filepath.Join(dir, "args.txt")
			t.Setenv("ARGS_OUT", argsOut)
			bin := filepath.Join(dir, "fake-evener")
			script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
	  printf '{"protocol":"evener-appwire-v5"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
			writeFakeEvener(t, bin, script)

			cfg := DefaultConfig()
			cfg.SpawnTimeout = 2 * time.Second
			cfg.APILog = tc.hubOn
			spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

			if _, err := spawner.Resume(context.Background(), hubcore.ResumeRequest{
				SessionID: hubtest.SessionID(t),
				Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
					APILog: tc.layerAPI,
				}},
				WorkingDir: dir,
				StateDir:   filepath.Join(dir, "state"),
			}); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			argsData, err := os.ReadFile(argsOut)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			args := strings.Fields(string(argsData))
			got := argValue(args, "--api-log")
			if got != tc.wantArg {
				t.Fatalf("--api-log = %q, want %q\nargs:\n%s", got, tc.wantArg, argsData)
			}
		})
	}
}

func TestLoadConfig_APILog(t *testing.T) {
	t.Run("defaults off when absent", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", filepath.Join(dir, "home"))
		cfg, err := LoadConfig(filepath.Join(dir, "nope.toml"))
		if err != nil {
			t.Fatalf("LoadConfig missing: %v", err)
		}
		if cfg.APILog {
			t.Error("APILog default: got true, want false (API-request logging is opt-in)")
		}
	})
	t.Run("explicit true decodes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hub.toml")
		if err := os.WriteFile(path, []byte("api_log = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if !cfg.APILog {
			t.Error("explicit api_log = true did not decode")
		}
	})
	t.Run("explicit false sticks", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hub.toml")
		if err := os.WriteFile(path, []byte("api_log = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.APILog {
			t.Error("explicit api_log = false was overridden back to true")
		}
	})
}

// TestLaunchResolveAppliesHubAPILogDefault pins preview/spawn consistency: with
// the hub.toml api_log floor on, evener/launch/resolve reports apiLog=true
// (provenance hub) rather than the builtin false that would contradict the
// daemon the hub actually spawns, and an explicit launch layer still wins in
// both directions.
func TestLaunchResolveAppliesHubAPILogDefault(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	emptyEnv := func(string) string { return "" }

	t.Run("hub default fills unset api_log", func(t *testing.T) {
		c := newHubLaunchControllerWithEnv(stateRoot, emptyEnv, true)
		got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Effective.APILog == nil || !*got.Effective.APILog {
			t.Fatalf("effective apiLog = %v, want true (hub floor)", got.Effective.APILog)
		}
		if got.Provenance["api_log"] != string(launchconfig.LayerHub) {
			t.Fatalf("api_log provenance = %q, want hub", got.Provenance["api_log"])
		}
	})

	t.Run("explicit layer false beats hub default", func(t *testing.T) {
		writer := newHubLaunchControllerWithEnv(stateRoot, emptyEnv, false)
		if _, err := writer.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
			CWD: cwd, Layer: "global",
			Config: appwire.LaunchConfigLayer{APILog: new(false)},
		}); err != nil {
			t.Fatalf("SetLayer: %v", err)
		}
		c := newHubLaunchControllerWithEnv(stateRoot, emptyEnv, true)
		got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Effective.APILog == nil || *got.Effective.APILog {
			t.Fatalf("effective apiLog = %v, want false (layer wins over hub floor)", got.Effective.APILog)
		}
		if got.Provenance["api_log"] != "global" {
			t.Fatalf("api_log provenance = %q, want global", got.Provenance["api_log"])
		}
	})

	t.Run("hub default off reports the pinned hub false", func(t *testing.T) {
		c := newHubLaunchControllerWithEnv(t.TempDir(), emptyEnv, false)
		got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: canonicalTempDir(t)})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Effective.APILog == nil || *got.Effective.APILog {
			t.Fatalf("effective apiLog = %v, want false (builtin)", got.Effective.APILog)
		}
		if got.Provenance["api_log"] != "hub" {
			t.Fatalf("api_log provenance = %q, want hub", got.Provenance["api_log"])
		}
	})
}

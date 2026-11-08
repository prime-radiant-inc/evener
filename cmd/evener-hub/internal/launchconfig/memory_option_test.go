package launchconfig

import (
	"reflect"
	"testing"
)

// checkToArgs_MemoryEnabled: memory_enabled reaches argv as the negated Bool
// flag evener serve takes. It is a tri-state — unset emits nothing (evener's
// own default, which is enabled), false disables, and true re-enables
// explicitly so a launch layer can override a disabling global default. The
// flag is a Bool flag, so the equals form is the only spelling that parses;
// the exact-argv table pins that no separate value element follows it.
func checkToArgs_MemoryEnabled(t *testing.T) {
	cases := []struct {
		name  string
		layer Layer
		want  []string
	}{
		{"unset", Layer{Model: "openai/gpt-5"}, []string{"--model", "openai/gpt-5", "--daemon-idle-timeout", "0s"}},
		{"explicit on", Layer{Model: "openai/gpt-5", MemoryEnabled: new(true)}, []string{"--model", "openai/gpt-5", "--disable-memory=false", "--daemon-idle-timeout", "0s"}},
		{"off", Layer{Model: "openai/gpt-5", MemoryEnabled: new(false)}, []string{"--model", "openai/gpt-5", "--disable-memory=true", "--daemon-idle-timeout", "0s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToArgs(Resolved{Effective: tc.layer})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ToArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// checkResolve_MemoryEnabledPrecedence: a global layer can turn memory off for
// every launch under the root, and a per-launch override turns it back on for
// one session. The file's key spelling is pinned by writing the TOML bytes
// directly.
func checkResolve_MemoryEnabledPrecedence(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths, err := PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, paths.Global, "model = \"openai/gpt-5\"\nmemory_enabled = false\n")

	resolved, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Effective.MemoryEnabled == nil || *resolved.Effective.MemoryEnabled {
		t.Fatalf("global off = %v, want false", resolved.Effective.MemoryEnabled)
	}
	if resolved.Provenance["memory_enabled"] != LayerGlobal {
		t.Fatalf("memory_enabled provenance = %v, want global", resolved.Provenance["memory_enabled"])
	}

	resolved, err = Resolve(stateRoot, cwd, Layer{MemoryEnabled: new(true)})
	if err != nil {
		t.Fatalf("Resolve with launch override: %v", err)
	}
	if resolved.Effective.MemoryEnabled == nil || !*resolved.Effective.MemoryEnabled {
		t.Fatalf("launch on over global off = %v, want true", resolved.Effective.MemoryEnabled)
	}
	if resolved.Provenance["memory_enabled"] != LayerLaunch {
		t.Fatalf("memory_enabled provenance = %v, want launch", resolved.Provenance["memory_enabled"])
	}
}

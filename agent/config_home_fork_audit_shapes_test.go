package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestConfigHomeForkAuditFindsEachShape runs the config-home fork audit over
// one fixture source file per shape and checks exactly which functions it
// flags. The fixtures are parsed, never compiled, so they name the package's
// real constructors and options without having to satisfy their signatures.
func TestConfigHomeForkAuditFindsEachShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		src     string
		flagged []string
	}{
		{
			// The home is isolated by a helper; the test's own body only
			// constructs.
			name: "helper isolates the config home",
			src: `
func isolateHome(t *testing.T) { t.Setenv("HOME", t.TempDir()) }
func TestViaHelper(t *testing.T) {
	isolateHome(t)
	_, _ = NewSession(nil, nil, nil, SessionConfig{})
}`,
			flagged: []string{"TestViaHelper"},
		},
		{
			// A config held in a variable is refused whatever it holds: only
			// the literal the constructor or option receives counts, and an
			// unrelated use of a gated variable is not a gate.
			name: "a config held in a variable is refused",
			src: `
func optionalSession(t *testing.T, opts ...sessionOpt) *Session {
	var o sessionOpts
	s, _ := NewSession(nil, nil, nil, o.cfg)
	return s
}
func TestVariableConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}
	_, _ = NewSession(nil, nil, nil, cfg)
}
func TestVariableThroughOption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}
	_ = optionalSession(t, withConfig(cfg))
}
func TestGatedVariableDerivesAnotherOption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}
	_ = optionalSession(t, withDir(cfg.StateDir))
}
func TestGatedVariableFieldInsideUngatedLiteral(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}
	_ = optionalSession(t, withConfig(SessionConfig{StateDir: cfg.StateDir}))
}
func TestLiteral(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _ = NewSession(nil, nil, nil, SessionConfig{testOnly: testConfig{skipGitSnapshot: true}})
}`,
			flagged: []string{"TestVariableConfig", "TestVariableThroughOption", "TestGatedVariableDerivesAnotherOption", "TestGatedVariableFieldInsideUngatedLiteral"},
		},
		{
			// Helpers: one gates unconditionally in its own body, one only on
			// an option (the newSession fixture's shape).
			name: "helpers gate by body or by option",
			src: `
func gatedSession(t *testing.T) *Session {
	s, _ := NewSession(nil, nil, nil, SessionConfig{testOnly: testConfig{skipGitSnapshot: true}})
	return s
}
func optionalSession(t *testing.T, opts ...sessionOpt) *Session {
	var o sessionOpts
	cfg := o.cfg
	if o.skipGitSnapshot {
		cfg.testOnly.skipGitSnapshot = true
	}
	s, _ := NewSession(nil, nil, nil, cfg)
	return s
}
func TestGatedHelper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = gatedSession(t)
}
func TestOptionalHelperWithOption(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = optionalSession(t, withoutGitSnapshot())
}
func TestOptionalHelperWithGatedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = optionalSession(t, withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}))
}
func TestOptionalHelperBare(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = optionalSession(t)
}
func TestOptionalHelperWithUngatedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = optionalSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1}))
}`,
			flagged: []string{"TestOptionalHelperBare", "TestOptionalHelperWithUngatedConfig"},
		},
		{
			// The restore entry points: the config-bearing one is gated by
			// its RestoreSessionConfig, the bare one cannot be gated at all.
			name: "restore entry points",
			src: `
func TestRestoreGated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _ = RestoreSessionFromMetaWithConfig(nil, nil, nil, meta, RestoreSessionConfig{testOnly: testConfig{skipGitSnapshot: true}})
}
func TestRestoreUngated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _ = RestoreSessionFromMetaWithConfig(nil, nil, nil, meta, RestoreSessionConfig{StateDir: dir})
}
func TestRestoreBare(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _ = RestoreSessionFromMeta(nil, nil, nil, meta, dir)
}`,
			flagged: []string{"TestRestoreBare", "TestRestoreUngated"},
		},
		{
			// No isolated home, or no constructor: nothing to say.
			name: "quiet without both halves",
			src: `
func TestOnlyHome(t *testing.T) { t.Setenv("HOME", t.TempDir()) }
func TestOnlyConstructor(t *testing.T) { _, _ = NewSession(nil, nil, nil, SessionConfig{}) }
func TestOtherEnv(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, _ = NewSession(nil, nil, nil, SessionConfig{})
}`,
			flagged: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := "package agent\n" + tc.src + "\n"
			if err := os.WriteFile(filepath.Join(dir, "shape_test.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			findings, err := configHomeForkAuditFindings(dir)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range findings {
				got = append(got, f[strings.LastIndex(f, " ")+1:])
			}
			sort.Strings(got)
			sort.Strings(tc.flagged)
			if strings.Join(got, ",") != strings.Join(tc.flagged, ",") {
				t.Fatalf("flagged %v, want %v\nfindings:\n%s", got, tc.flagged, strings.Join(findings, "\n"))
			}
		})
	}
}

package main

import (
	"reflect"
	"testing"
)

func lintEnv(vals map[string]string) func(string) string {
	return func(name string) string { return vals[name] }
}

func TestParseLintConfigDefaults(t *testing.T) {
	cfg, diag := parseLintConfig(lintEnv(nil))
	if diag != "" {
		t.Fatalf("defaults produced a diagnostic: %q", diag)
	}
	// fuzz included: every Go module in the workspace is linted, unlike the
	// test gate, which the fuzz module sits out.
	wantModules := []string{".", "agent", "llm", "auth", "envvars", "invariant", "identifier", "fuzz"}
	if !reflect.DeepEqual(cfg.Modules, wantModules) {
		t.Errorf("default modules = %v, want %v", cfg.Modules, wantModules)
	}
	if cfg.Parallel != 4 {
		t.Errorf("default parallel = %d, want 4", cfg.Parallel)
	}
}

func TestParseLintConfigSplitsModulesPreservingOrder(t *testing.T) {
	cfg, diag := parseLintConfig(lintEnv(map[string]string{"MODULES": "identifier  agent\tllm ."}))
	if diag != "" {
		t.Fatalf("unexpected diagnostic: %q", diag)
	}
	want := []string{"identifier", "agent", "llm", "."}
	if !reflect.DeepEqual(cfg.Modules, want) {
		t.Errorf("modules = %v, want %v (caller order is the interface)", cfg.Modules, want)
	}
}

func TestParseLintConfigEmptyParallelUsesDefault(t *testing.T) {
	cfg, diag := parseLintConfig(lintEnv(map[string]string{"LINT_PARALLEL": ""}))
	if diag != "" || cfg.Parallel != 4 {
		t.Errorf("empty LINT_PARALLEL = (%d, %q), want (4, \"\")", cfg.Parallel, diag)
	}
}

func TestParseLintConfigAcceptsPositiveIntegers(t *testing.T) {
	for _, v := range []string{"1", "2", "10", "16"} {
		cfg, diag := parseLintConfig(lintEnv(map[string]string{"LINT_PARALLEL": v}))
		if diag != "" {
			t.Errorf("LINT_PARALLEL=%s produced diagnostic %q", v, diag)
		}
		if got := cfg.Parallel; got == 0 {
			t.Errorf("LINT_PARALLEL=%s parsed to 0", v)
		}
	}
}

func TestParseLintConfigRejectsNonPositiveAndLeadingZeroes(t *testing.T) {
	// 08 and 010 matter: strconv would accept them as 8 and 10, silently
	// changing what the shell contract rejected.
	// The overflow value is digits-only with no leading zero, so the shell
	// contract's pattern passed it into wrapping arithmetic; here it must be
	// rejected outright rather than wrapped into a negative wave stride.
	for _, v := range []string{"0", "-1", "nope", "00", "08", "010", "4 ", "99999999999999999999"} {
		_, diag := parseLintConfig(lintEnv(map[string]string{"LINT_PARALLEL": v}))
		if diag == "" {
			t.Errorf("LINT_PARALLEL=%q was accepted", v)
			continue
		}
		want := "lint: LINT_PARALLEL must be a positive integer without leading zeroes (got " + v + ")"
		if diag != want {
			t.Errorf("LINT_PARALLEL=%q diagnostic = %q, want %q", v, diag, want)
		}
	}
}

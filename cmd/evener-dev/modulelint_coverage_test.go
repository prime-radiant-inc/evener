package main

import (
	"testing"
)

// TestIsPositiveInteger covers the isPositiveInteger function.
func TestIsPositiveInteger(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"1", true},
		{"9", true},
		{"10", true},
		{"123", true},
		{"", false},
		{"0", false},
		{"01", false},
		{"08", false},
		{"-1", false},
		{"abc", false},
		{"1a", false},
	}
	for _, c := range cases {
		if got := isPositiveInteger(c.s); got != c.want {
			t.Errorf("isPositiveInteger(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// TestParseLintConfigDefaultValues covers the default-values path.
func TestParseLintConfigDefaultValues(t *testing.T) {
	cfg, diag := parseLintConfig(func(string) string { return "" })
	if diag != "" {
		t.Fatalf("unexpected diagnostic: %q", diag)
	}
	if len(cfg.Modules) != 8 {
		t.Fatalf("default modules = %v, want 8 entries", cfg.Modules)
	}
	if cfg.Parallel != defaultLintParallel {
		t.Fatalf("parallel = %d, want %d", cfg.Parallel, defaultLintParallel)
	}
}

// TestParseLintConfigCustomModulesValue covers the custom-modules path.
func TestParseLintConfigCustomModulesValue(t *testing.T) {
	cfg, diag := parseLintConfig(func(name string) string {
		if name == "MODULES" {
			return "agent llm"
		}
		return ""
	})
	if diag != "" {
		t.Fatalf("unexpected diagnostic: %q", diag)
	}
	if len(cfg.Modules) != 2 || cfg.Modules[0] != "agent" || cfg.Modules[1] != "llm" {
		t.Fatalf("modules = %v, want [agent llm]", cfg.Modules)
	}
}

// TestParseLintConfigCustomParallelValue covers the custom-parallel path.
func TestParseLintConfigCustomParallelValue(t *testing.T) {
	cfg, diag := parseLintConfig(func(name string) string {
		if name == "LINT_PARALLEL" {
			return "8"
		}
		return ""
	})
	if diag != "" {
		t.Fatalf("unexpected diagnostic: %q", diag)
	}
	if cfg.Parallel != 8 {
		t.Fatalf("parallel = %d, want 8", cfg.Parallel)
	}
}

// TestParseLintConfigInvalidParallelValue covers the invalid-parallel error path.
func TestParseLintConfigInvalidParallelValue(t *testing.T) {
	_, diag := parseLintConfig(func(name string) string {
		if name == "LINT_PARALLEL" {
			return "0"
		}
		return ""
	})
	if diag == "" {
		t.Fatalf("expected diagnostic for invalid parallel")
	}
}

// TestParseLintConfigNonNumericParallelValue covers the non-numeric parallel error.
func TestParseLintConfigNonNumericParallelValue(t *testing.T) {
	_, diag := parseLintConfig(func(name string) string {
		if name == "LINT_PARALLEL" {
			return "abc"
		}
		return ""
	})
	if diag == "" {
		t.Fatalf("expected diagnostic for non-numeric parallel")
	}
}

// TestParseLintConfigLeadingZeroParallelValue covers the leading-zero rejection.
func TestParseLintConfigLeadingZeroParallelValue(t *testing.T) {
	_, diag := parseLintConfig(func(name string) string {
		if name == "LINT_PARALLEL" {
			return "08"
		}
		return ""
	})
	if diag == "" {
		t.Fatalf("expected diagnostic for leading-zero parallel")
	}
}

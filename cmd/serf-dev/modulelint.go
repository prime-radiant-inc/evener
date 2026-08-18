package main

import (
	"strconv"
	"strings"
)

// The defaults are the interface run-module-lint.sh shipped with; the
// Makefile's lint-golangci target overrides MODULES with $(GO_MODULES), and
// operators rely on the bare invocation checking the canonical set.
const (
	defaultLintModules  = ". agent llm auth envvars invariant identifier"
	defaultLintParallel = 4
)

type lintConfig struct {
	Modules  []string
	Parallel int
}

// parseLintConfig reads the env-only interface: MODULES (whitespace-split,
// caller order preserved) and LINT_PARALLEL. An empty variable means its
// default. The returned diagnostic is non-empty exactly when the
// configuration is unusable; the caller prints it and summarizes as a setup
// failure without creating anything.
func parseLintConfig(getenv func(string) string) (lintConfig, string) {
	modules := getenv("MODULES")
	if modules == "" {
		modules = defaultLintModules
	}
	parallel := defaultLintParallel
	if v := getenv("LINT_PARALLEL"); v != "" {
		n, err := strconv.Atoi(v)
		if !isPositiveInteger(v) || err != nil {
			return lintConfig{}, "lint: LINT_PARALLEL must be a positive integer without leading zeroes (got " + v + ")"
		}
		parallel = n
	}
	return lintConfig{Modules: strings.Fields(modules), Parallel: parallel}, ""
}

// isPositiveInteger accepts exactly the shell contract's ^[1-9][0-9]*$:
// strconv would take "08" as 8, silently blessing a value the interface
// rejects.
func isPositiveInteger(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	for _, d := range s[1:] {
		if d < '0' || d > '9' {
			return false
		}
	}
	return true
}

func lintMain(args []string) int {
	return 1
}

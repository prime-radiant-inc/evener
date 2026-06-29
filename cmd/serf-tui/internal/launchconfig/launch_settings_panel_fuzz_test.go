package launchconfig

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

// parseHeavyFields are the launch-config fields whose edit handler runs a real
// value parser (parseOptionalInt / parseOptionalBool / parseMCPs JSON /
// parseEnvMap / parseModelFallbacks). Path-validated fields are deliberately
// excluded so the fuzz stays deterministic and FS-free.
var parseHeavyFields = []string{
	"max_rounds", "max_subagent_depth", "app_replay_size",
	"no_project_prompts", "verbose", "raw_http_logging",
	"mcps", "env", "model_fallbacks",
	"model", "reasoning_effort", "system_prompt_text",
}

// FuzzApplyEdit drives the panel's real edit-value parser. applyEdit dispatches
// on the field name and converts the user-typed string into a typed
// LaunchConfigLayer field via the package's parse helpers. Fuzzing (fieldIdx,
// value) over the parse-heavy fields exercises every decoder. Oracle: no-panic
// floor plus an error/consistency invariant — a non-nil error must leave the
// returned layer usable (the function returns the input layer on error).
func FuzzApplyEdit(f *testing.F) {
	seeds := []struct {
		idx int
		val string
	}{
		{0, "200"}, {0, "not-an-int"}, {0, "(default)"}, {0, "-3"},
		{3, "true"}, {3, "no"}, {3, "maybe"}, {3, "(default)"},
		{6, `[{"name":"a","command":"c","args":["-x"]}]`},
		{6, `{"name":"a","command":"c"}`},
		{6, "name=cmd arg1 arg2"},
		{6, "not json [ broken"},
		{7, "FOO=bar, BAZ=qux"}, {7, "= = ="}, {7, ""},
		{8, "a,b,c"}, {8, "[]"}, {8, "(default)"},
		{9, "  anthropic/claude-haiku-4-5 "},
		{11, "multi\nline\ntext"},
	}
	for _, s := range seeds {
		f.Add(s.idx, s.val)
	}

	f.Fuzz(func(t *testing.T, fieldIdx int, value string) {
		if fieldIdx < 0 {
			fieldIdx = -fieldIdx
		}
		field := parseHeavyFields[fieldIdx%len(parseHeavyFields)]

		updated, err := applyEdit(appwire.LaunchConfigLayer{}, field, value)
		if err != nil {
			// On error the contract is to return the (unmodified) input layer;
			// re-applying the same edit must still error, not panic.
			if _, err2 := applyEdit(updated, field, value); err2 == nil {
				// A previously-rejected value becoming valid on a zero layer would
				// be a non-deterministic parser; only fail if that happens.
				t.Fatalf("applyEdit(%q,%q) error then success: %v", field, value, err)
			}
			return
		}
		// A successful edit must round-trip its own re-application without panic.
		_, _ = applyEdit(updated, field, value)
	})
}

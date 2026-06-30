package launchconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// applyEditFields are the launch-config fields whose edit handler runs a real
// value parser or path validator. Indices 0–11 are FS-free parse-heavy fields
// (parseOptionalInt / parseOptionalBool / parseMCPs / parseEnvMap /
// parseModelFallbacks); indices 12+ are path-validated fields whose handler
// calls validateLocalLaunchPath. Path values are anchored inside a t.TempDir
// sandbox (see anchorValue) so the validator only ever stats sandbox paths.
// Appending the path fields keeps the existing index-addressed seeds valid.
var applyEditFields = []string{
	"max_rounds", "max_subagent_depth", "app_replay_size", // 0,1,2
	"no_project_prompts", "verbose", "raw_http_logging", // 3,4,5
	"mcps", "env", "model_fallbacks", // 6,7,8
	"model", "reasoning_effort", "system_prompt_text", // 9,10,11
	"skills_dirs", "plugin_dirs", "mcp_configs", // 12,13,14
	"trace_file", "cpu_profile", "export_atif_path", // 15,16,17
	"system_prompt_file", "system_prompt_append_file", // 18,19
}

// pathFields are the indices into applyEditFields whose handler validates a
// path on the filesystem.
func isPathField(field string) bool {
	switch field {
	case "skills_dirs", "plugin_dirs", "mcp_configs",
		"trace_file", "cpu_profile", "export_atif_path",
		"system_prompt_file", "system_prompt_append_file":
		return true
	default:
		return false
	}
}

// listPathField reports whether a path field accepts a comma-separated list.
func listPathField(field string) bool {
	switch field {
	case "skills_dirs", "plugin_dirs", "mcp_configs":
		return true
	default:
		return false
	}
}

// FuzzApplyEdit drives the panel's real edit-value parser across every editable
// field, including the path-validated ones (skills_dirs, plugin_dirs,
// mcp_configs, trace_file, cpu_profile, export_atif_path, system_prompt_file,
// system_prompt_append_file). applyEdit dispatches on the field name and
// converts the user-typed string into a typed LaunchConfigLayer field. For path
// fields the fuzzed value is anchored inside a throwaway sandbox so
// validateLocalLaunchPath never resolves a real filesystem path.
//
// Oracles (real post-conditions, not bare never-panic):
//   - error contract: a non-nil error must leave the returned layer == the
//     unmodified input (applyEdit returns the input layer on every error path).
//   - determinism: the same (field, value) must produce the same (layer, error)
//     — no nondeterministic parse/validate.
//   - no-escape: for path fields, every stored path stays inside the sandbox
//     root; applyEdit never yields a path outside it.
//   - wire round-trip: the resulting layer survives the SetLayer marshalling
//     (LaunchConfigLayer.MarshalJSON) with byte-stable JSON.
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
		// Path fields, anchored into the sandbox by anchorValue.
		{12, "dir"}, {12, "file"}, {12, "missing"}, {12, "../escape"},
		{12, "dir,file,missing"}, {12, "(default)"}, {12, ""},
		{14, "file"}, {14, "dir"},
		{15, "out"}, {15, "dir"}, {15, "(default)"},
		{18, "file"}, {18, "missing"}, {18, "../../etc/passwd"},
	}
	for _, s := range seeds {
		f.Add(s.idx, s.val)
	}

	f.Fuzz(func(t *testing.T, fieldIdx int, value string) {
		if fieldIdx < 0 {
			fieldIdx = -fieldIdx
		}
		field := applyEditFields[fieldIdx%len(applyEditFields)]

		var root string
		if isPathField(field) {
			root = newSandbox(t)
			value = anchorValue(field, root, value)
		}

		empty := appwire.LaunchConfigLayer{}
		updated, err := applyEdit(empty, field, value)
		if err != nil {
			// Error contract: the input layer is returned unmodified.
			if !reflect.DeepEqual(updated, empty) {
				t.Fatalf("applyEdit(%q,%q) errored but mutated the layer: %#v", field, value, updated)
			}
			// Determinism: the same edit fails the same way (never panics).
			updated2, err2 := applyEdit(empty, field, value)
			if err2 == nil || !reflect.DeepEqual(updated2, empty) {
				t.Fatalf("applyEdit(%q,%q) nondeterministic: error then %v / %#v", field, value, err2, updated2)
			}
			return
		}

		// Determinism on success.
		updated2, err2 := applyEdit(empty, field, value)
		if err2 != nil || !reflect.DeepEqual(updated, updated2) {
			t.Fatalf("applyEdit(%q,%q) nondeterministic on success: %v / %#v vs %#v", field, value, err2, updated, updated2)
		}

		if isPathField(field) {
			assertPathsWithin(t, root, updated, field, value)
		}

		// SetLayer ships the layer over the wire; the marshalling must round-trip.
		assertWireRoundTrip(t, updated, field, value)
	})
}

// newSandbox builds a throwaway directory tree with a directory, a regular
// file, and an executable so the path-validator's dir/file/exec branches are
// reachable entirely inside the sandbox.
func newSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatalf("mkdir sandbox dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "exe"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write sandbox exe: %v", err)
	}
	return root
}

// anchorValue rewrites a fuzzed path value so it can only resolve inside root.
// Each entry is joined onto root; an entry that would escape root is replaced
// with a clearly-relative token so validateLocalLaunchPath rejects it at the
// IsAbs check before ever calling os.Stat. Empty and "(default)" entries pass
// through to exercise the clearing branches.
func anchorValue(field, root, value string) string {
	if listPathField(field) {
		parts := strings.Split(value, ",")
		for i, p := range parts {
			parts[i] = anchorOne(root, p)
		}
		return strings.Join(parts, ",")
	}
	return anchorOne(root, value)
}

func anchorOne(root, s string) string {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "", "(default)":
		return trimmed
	}
	cand := filepath.Clean(filepath.Join(root, trimmed))
	if withinRoot(root, cand) {
		return cand
	}
	// Would escape the sandbox: hand the validator a relative path, which it
	// rejects ("absolute path required") without touching the filesystem.
	return "relative-rejected"
}

// assertPathsWithin verifies every path the layer now stores resolves inside
// the sandbox root.
func assertPathsWithin(t *testing.T, root string, layer appwire.LaunchConfigLayer, field, value string) {
	t.Helper()
	var paths []string
	paths = append(paths, layer.SkillsDirs...)
	paths = append(paths, layer.PluginDirs...)
	paths = append(paths, layer.MCPConfigs...)
	for _, p := range []string{layer.TraceFile, layer.CPUProfile, layer.ExportATIFPath, layer.SystemPromptFile, layer.SystemPromptAppendFile} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		if !withinRoot(root, p) {
			t.Fatalf("applyEdit(%q,%q) stored a path outside the sandbox: %q not within %q", field, value, p, root)
		}
	}
}

// withinRoot reports whether p is an absolute path inside root.
func withinRoot(root, p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil {
		return false
	}
	slash := filepath.ToSlash(rel)
	return slash != ".." && !strings.HasPrefix(slash, "../")
}

// assertWireRoundTrip exercises the SetLayer marshalling path
// (LaunchConfigLayer.MarshalJSON) and asserts the wire form is a fixed point
// once it has been normalized. The first marshal is lossy for inputs the TUI's
// text box would never produce (invalid UTF-8 collapses to U+FFFD), so — like
// the decode-target round-trips — we compare the two POST-normalization forms:
// the normalized encoding must be byte-stable and the decoded value identical
// across a further round-trip. A genuine MarshalJSON defect (dropped,
// duplicated, or reordered fields) still reddens this.
func assertWireRoundTrip(t *testing.T, layer appwire.LaunchConfigLayer, field, value string) {
	t.Helper()
	raw1, err := json.Marshal(layer)
	if err != nil {
		t.Fatalf("applyEdit(%q,%q): marshal layer: %v", field, value, err)
	}
	var d1 appwire.LaunchConfigLayer
	if err := json.Unmarshal(raw1, &d1); err != nil {
		t.Fatalf("applyEdit(%q,%q): unmarshal layer: %v\n json=%s", field, value, err, raw1)
	}
	raw2, err := json.Marshal(d1)
	if err != nil {
		t.Fatalf("applyEdit(%q,%q): re-marshal layer: %v", field, value, err)
	}
	var d2 appwire.LaunchConfigLayer
	if err := json.Unmarshal(raw2, &d2); err != nil {
		t.Fatalf("applyEdit(%q,%q): unmarshal normalized layer: %v\n json=%s", field, value, err, raw2)
	}
	raw3, err := json.Marshal(d2)
	if err != nil {
		t.Fatalf("applyEdit(%q,%q): re-marshal normalized layer: %v", field, value, err)
	}
	if !bytes.Equal(raw2, raw3) {
		t.Fatalf("applyEdit(%q,%q): normalized wire form not byte-stable:\n once=%s\n twice=%s", field, value, raw2, raw3)
	}
	if !reflect.DeepEqual(d1, d2) {
		t.Fatalf("applyEdit(%q,%q): decoded layer not stable:\n once=%#v\n twice=%#v", field, value, d1, d2)
	}
}

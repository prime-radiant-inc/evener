package launchconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzLaunchConfigResolve promotes the decode-only corpus to drive the real
// resolver. It plants the fuzzed bytes as an on-disk launch layer inside a
// throwaway sandbox (a state root + a repo root, both t.TempDir) and runs
// Resolve, exercising LoadLayer, the trust gate, validateAndExpandRepoLayer,
// validateAbsolutePaths, and mergeLayers — none of which the decode-only
// FuzzLaunchConfigDecode reaches. Two arms, selected by `which`:
//
//	even — the fuzzed bytes are the in-repo .serf/launch.toml, marked trusted
//	       via a matching meta.toml, so a cleanly-hashing layer flows through
//	       validateAndExpandRepoLayer (repo-relative expansion + escape
//	       rejection).
//	odd  — the fuzzed bytes are the global layer, flowing through
//	       validateAbsolutePaths.
//
// Oracles (real post-conditions, not bare never-panic):
//   - determinism: Resolve is a pure function of its on-disk inputs, so two
//     runs over the identical sandbox must agree on error-ness and produce
//     DeepEqual results.
//   - no-escape: every path the trusted repo layer contributes after expansion
//     stays inside the repo root — nothing resolves outside the sandbox.
//   - absolute-only: every path the global layer contributes is absolute
//     (validateAbsolutePaths drops the rest).
func FuzzLaunchConfigResolve(f *testing.F) {
	f.Add(0, []byte("skills_dirs = [\"skills\"]\nplugin_dirs = [\"plugins\"]\n"))
	f.Add(0, []byte("skills_dirs = [\"../escape\"]\n"))
	f.Add(0, []byte("mcp_configs = [\"a\", \"../../b\"]\n"))
	f.Add(0, []byte("trace_file = \"out/trace.json\"\ncpu_profile = \"prof\"\n"))
	f.Add(0, []byte("system_prompt_append = [\"docs/extra.md\"]\n"))
	f.Add(0, []byte("model = \"gpt-5.5\"\n[env]\nFOO = \"bar\"\n"))
	f.Add(0, []byte("[env]\nOPENAI_API_KEY = \"leak\"\n"))
	f.Add(1, []byte("skills_dirs = [\"/abs/ok\", \"rel/bad\"]\n"))
	f.Add(1, []byte("trace_file = \"/abs/trace\"\nplugin_dirs = [\"rel\"]\n"))
	f.Add(0, []byte(""))
	f.Add(0, []byte("= = ="))
	f.Add(0, []byte("max_rounds = \"not an int\""))
	for _, s := range edgeseeds.TOML() {
		f.Add(0, s)
		f.Add(1, s)
	}
	f.Add(0, edgeseeds.TOMLFeatureDoc())

	f.Fuzz(func(t *testing.T, which int, raw []byte) {
		stateRoot := t.TempDir()
		cwd := t.TempDir()

		if which&1 == 0 {
			plantRepoLayer(t, stateRoot, cwd, raw)
		} else {
			if err := os.WriteFile(filepath.Join(stateRoot, "launch.toml"), raw, 0o600); err != nil {
				t.Fatalf("write global layer: %v", err)
			}
		}

		got, err := Resolve(stateRoot, cwd, Layer{})
		got2, err2 := Resolve(stateRoot, cwd, Layer{})
		if (err == nil) != (err2 == nil) {
			t.Fatalf("Resolve error nondeterministic: %v vs %v\n input=%q which=%d", err, err2, raw, which)
		}
		if err != nil {
			// A malformed global/project layer is rejected by LoadLayer with a
			// hard error; the repo arm never errors (parse failures there go to
			// diagnostics). Either way, nothing further to assert.
			return
		}
		if !reflect.DeepEqual(got, got2) {
			t.Fatalf("Resolve not deterministic:\n input=%q which=%d\n once=%#v\n twice=%#v", raw, which, got, got2)
		}

		if which&1 == 0 {
			if repo, ok := got.Layers[LayerRepo]; ok {
				assertNoEscape(t, cwd, repo, raw)
			}
		} else {
			if global, ok := got.Layers[LayerGlobal]; ok {
				assertAllAbsolute(t, global, raw)
			}
		}
	})
}

// plantRepoLayer writes raw as the in-repo launch.toml and, when it hashes
// cleanly, records a trusted meta.toml so loadRepoLayer takes the trusted
// branch into validateAndExpandRepoLayer.
func plantRepoLayer(t *testing.T, stateRoot, cwd string, raw []byte) {
	t.Helper()
	repoDir := filepath.Join(cwd, ".serf")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "launch.toml"), raw, 0o600); err != nil {
		t.Fatalf("write repo layer: %v", err)
	}
	hash, err := CanonicalHashTOML(raw)
	if err != nil {
		return // malformed: resolver keeps it untrusted, expansion is skipped
	}
	meta := Meta{
		Schema:    1,
		CWD:       cwd,
		CreatedAt: time.Unix(0, 0).UTC(),
		Trust: MetaTrust{
			Hashes:    []string{hash},
			Decision:  "trusted",
			DecidedAt: time.Unix(0, 0).UTC(),
		},
	}
	metaPath := filepath.Join(stateRoot, "projects", ProjectID(cwd), "meta.toml")
	if err := SaveMeta(metaPath, meta); err != nil {
		t.Fatalf("save meta: %v", err)
	}
}

// assertNoEscape verifies every path the (trusted) repo layer contributes was
// expanded to an absolute path that stays inside the repo root.
func assertNoEscape(t *testing.T, root string, layer Layer, raw []byte) {
	t.Helper()
	for _, p := range layerPaths(layer) {
		if !withinRoot(root, p) {
			t.Fatalf("repo layer path escapes sandbox: %q not within %q\n input=%q", p, root, raw)
		}
	}
}

// assertAllAbsolute verifies validateAbsolutePaths dropped every relative path
// from the global layer.
func assertAllAbsolute(t *testing.T, layer Layer, raw []byte) {
	t.Helper()
	for _, p := range layerPaths(layer) {
		if !filepath.IsAbs(p) {
			t.Fatalf("global layer kept relative path %q\n input=%q", p, raw)
		}
	}
}

// layerPaths collects every non-empty path-bearing field of a layer.
func layerPaths(layer Layer) []string {
	var paths []string
	paths = append(paths, layer.SkillsDirs...)
	paths = append(paths, layer.PluginDirs...)
	paths = append(paths, layer.MCPConfigs...)
	paths = append(paths, layer.SystemPromptAppend...)
	for _, p := range []string{
		layer.SystemPromptFile, layer.SystemPromptAppendFile,
		layer.TraceFile, layer.CPUProfile, layer.ExportATIFPath,
	} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
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

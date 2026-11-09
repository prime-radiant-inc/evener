//go:build evenerfuzz

package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/frontmatter"
)

// FuzzSkillDiscoveryProgram drives the complete local-skill lifecycle through
// a real temporary filesystem: root-to-cwd discovery, ordered extra-directory
// shadowing, and bundled-skill caching. The
// execution-environment boundary is a scripted FakeEnv, so Git root lookup is
// deterministic and never launches Git. It does not contact a provider, start
// a process, or touch files outside t.TempDir and the package-owned temporary
// bundled-skill directories.
func FuzzSkillDiscoveryProgram(f *testing.F) {
	f.Add(uint8(0), "root body", "---\nname: only-name\n---\n")
	f.Add(uint8(1), "body with\nmultiple lines", "not frontmatter")
	f.Add(uint8(2), "", "---\nname: raw\ndescription: raw description\n---\nraw body")

	f.Fuzz(func(t *testing.T, shape uint8, body, raw string) {
		body = skillProgramLimit(body)
		raw = skillProgramLimit(raw)

		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve temp root: %v", err)
		}
		project := filepath.Join(root, "project")
		cwd := filepath.Join(project, "nested")
		extraFirst := t.TempDir()
		extraLast := t.TempDir()

		rootShared := skillProgramDocument("shared", "root shared skill", body+"|root", shape)
		projectShared := skillProgramDocument("shared", "project shared skill", body+"|project", shape+1)
		cwdShared := skillProgramDocument("shared", "nested shared skill", body+"|nested", shape+2)
		extraFirstShared := skillProgramDocument("shared", "first extra shared skill", body+"|extra-first", shape+3)
		extraLastShared := skillProgramDocument("shared", "last extra shared skill", body+"|extra-last", shape+4)
		rootOnly := skillProgramDocument("root-only", "root-only skill", body+"|root-only", shape+5)
		projectOnly := skillProgramDocument("project-only", "project-only skill", body+"|project-only", shape+6)
		cwdOnly := skillProgramDocument("cwd-only", "cwd-only skill", body+"|cwd-only", shape+7)
		extraOnly := skillProgramDocument("extra-only", "extra-only skill", body+"|extra-only", shape+8)

		rootSkills := filepath.Join(root, "skills")
		projectSkills := filepath.Join(project, "skills")
		cwdSkills := filepath.Join(cwd, "skills")
		skillProgramWrite(t, rootSkills, "shared", rootShared)
		skillProgramWrite(t, rootSkills, "root-only", rootOnly)
		skillProgramWrite(t, projectSkills, "shared", projectShared)
		skillProgramWrite(t, projectSkills, "project-only", projectOnly)
		skillProgramWrite(t, cwdSkills, "shared", cwdShared)
		skillProgramWrite(t, cwdSkills, "cwd-only", cwdOnly)
		skillProgramWrite(t, extraFirst, "shared", extraFirstShared)
		skillProgramWrite(t, extraLast, "shared", extraLastShared)
		skillProgramWrite(t, extraLast, "extra-only", extraOnly)

		// This direct-only directory supplies malformed, missing, and loose-file
		// cases without allowing arbitrary fuzz bytes to change the discovery
		// program's expected shadowing result.
		direct := t.TempDir()
		rawPath := skillProgramWrite(t, direct, "raw", raw)
		invalidPath := skillProgramWrite(t, direct, "invalid", "---\n: bad: yaml: [unclosed\n---\n")
		invalidControlsPath := skillProgramWrite(t, direct, "badcontrols", "---\nname: badcontrols\ndescription: invalid controls\nallowed-tools:\n  - 7\n---\nbody\n")
		if err := os.WriteFile(filepath.Join(direct, "loose.md"), []byte("not a skill directory"), 0o644); err != nil {
			t.Fatalf("write loose file: %v", err)
		}
		directSkills := map[string]SkillMeta{}
		ScanSkillsDir(direct, directSkills)
		ScanSkillsDir(filepath.Join(direct, "missing"), directSkills)
		// Invalid controls mark the skill unavailable (TestSkillDiscoveryInvalidWinnerAndDiagnostics),
		// so the scan view excludes it just like a malformed document.
		if _, ok := directSkills["badcontrols"]; ok {
			t.Fatal("ScanSkillsDir advertised a skill with invalid allowed-tools controls")
		}
		if _, ok := parseSkillFile(invalidControlsPath); ok {
			t.Fatal("parseSkillFile accepted invalid allowed-tools controls")
		}
		if _, ok := parseSkillFile(filepath.Join(direct, "missing", "SKILL.md")); ok {
			t.Fatal("missing SKILL.md parsed successfully")
		}
		if _, err := loadSkillBody(SkillMeta{SkillFile: filepath.Join(direct, "missing", "SKILL.md")}); err == nil {
			t.Fatal("LoadSkillBody missing file succeeded")
		}
		if _, err := loadSkillBody(SkillMeta{SkillFile: invalidPath}); err == nil {
			t.Fatal("LoadSkillBody invalid frontmatter succeeded")
		}
		if meta, ok := parseSkillFile(rawPath); ok {
			// LoadSkillBody validates the recorded identity against the file's
			// declared identity, so it must be handed the meta discovery recorded
			// (carrying the declared name), never a bare path.
			if _, err := loadSkillBody(meta); err != nil {
				t.Fatalf("LoadSkillBody rejected a skill parseSkillFile accepted: %v", err)
			}
		}

		env := &agenttest.FakeEnv{WorkDir: cwd, GitRoot: root}
		skills := skillProgramDiscover(t, env, extraFirst, filepath.Join(root, "missing-extra"), extraLast)
		skillProgramAssertDiscovered(t, skills, "shared", extraLastShared, extraLast)
		skillProgramAssertDiscovered(t, skills, "root-only", rootOnly, rootSkills)
		skillProgramAssertDiscovered(t, skills, "project-only", projectOnly, projectSkills)
		skillProgramAssertDiscovered(t, skills, "cwd-only", cwdOnly, cwdSkills)
		skillProgramAssertDiscovered(t, skills, "extra-only", extraOnly, extraLast)
		if len(skills) != 5 {
			t.Fatalf("Discover names = %v, want five expected skills", skillProgramNames(skills))
		}

		// A no-Git fake keeps discovery local to cwd; the fake's failure result is
		// the only command behavior involved.
		local := skillProgramDiscover(t, &agenttest.FakeEnv{WorkDir: cwd}, filepath.Join(root, "missing-extra"))
		skillProgramAssertDiscovered(t, local, "shared", cwdShared, cwdSkills)
		skillProgramAssertDiscovered(t, local, "cwd-only", cwdOnly, cwdSkills)
		if len(local) != 2 {
			t.Fatalf("local-only skills = %v, want nested skills", skillProgramNames(local))
		}
		if got := skillProgramDiscover(t, nil); len(got) != 0 {
			t.Fatalf("Discover(nil env) = %v, want no skills", got)
		}
		if got := skillProgramDiscover(t, &agenttest.FakeEnv{WorkDir: " \t "}); len(got) != 0 {
			t.Fatalf("Discover blank cwd = %v, want no skills", got)
		}
		if got := skillProgramDiscover(t, &agenttest.FakeEnv{WorkDir: filepath.Join(root, "does-not-exist")}); len(got) != 0 {
			t.Fatalf("Discover absent cwd = %v, want no skills", got)
		}

		firstDir, err := EmbeddedSkillsDir()
		if err != nil {
			t.Fatalf("EmbeddedSkillsDir first: %v", err)
		}
		secondDir, err := EmbeddedSkillsDir()
		if err != nil || firstDir != secondDir {
			t.Fatalf("EmbeddedSkillsDir cache = %q, %q, %v", firstDir, secondDir, err)
		}
		if info, err := os.Stat(firstDir); err != nil || !info.IsDir() {
			t.Fatalf("EmbeddedSkillsDir output = %q, %v", firstDir, err)
		}

		cachedSkills := map[string]SkillMeta{}
		ScanSkillsDir(firstDir, cachedSkills)
		if len(cachedSkills) == 0 {
			t.Fatal("EmbeddedSkillsDir holds no discoverable skills")
		}
		for cachedName, cachedMeta := range cachedSkills {
			if _, err := loadSkillBody(cachedMeta); err != nil {
				t.Fatalf("Load bundled %q: %v", cachedName, err)
			}
			break
		}

		original := map[string]SkillMeta{"copy": {Name: "copy", Description: "copy", SkillFile: rawPath}}
		clone := cloneSkillMetaMap(original)
		clone["copy"] = SkillMeta{Name: "mutated"}
		if original["copy"].Name != "copy" {
			t.Fatal("cloneSkillMetaMap exposed the original map")
		}

		// A publish failure must be reported rather than hand back a stale or
		// empty cache: point the materializer at a base that cannot be used.
		// The restore is registered as cleanup so a t.Fatal above cannot leave
		// the globals pointing at this case's temporary directory.
		savedBase := embeddedSkillsBaseDir
		saved := saveEmbeddedSkillsCache()
		t.Cleanup(func() {
			restoreEmbeddedSkillsCache(saved)
			embeddedSkillsBaseDir = savedBase
		})
		embeddedSkillsBaseDir = func() (string, error) { return filepath.Join(root, "missing-base"), nil }
		embeddedSkillsCache.mu.Lock()
		if embeddedSkillsCache.lease != nil {
			_ = embeddedSkillsCache.lease.Release()
		}
		embeddedSkillsCache.dir = ""
		embeddedSkillsCache.digest = ""
		embeddedSkillsCache.skills = nil
		embeddedSkillsCache.verified = false
		embeddedSkillsCache.lease = nil
		embeddedSkillsCache.leasedDir = ""
		_, cacheErr := ensureEmbeddedSkillsLocked()
		embeddedSkillsCache.mu.Unlock()
		if cacheErr == nil {
			t.Fatal("shared-cache publish failure was not reported")
		}
		// The bundled skills survive the failure: resolution serves the
		// process-lifetime extraction instead of nothing. A platform that refuses
		// its process root (Windows) fails closed instead, so its harness case
		// asserts only the reported publish failure above.
		if !processCopyRootRefused(os.TempDir()) {
			processDir, err := EmbeddedSkillsDir()
			if err != nil {
				t.Fatalf("EmbeddedSkillsDir publish failure: %v", err)
			}
			processSkills := map[string]SkillMeta{}
			ScanSkillsDir(processDir, processSkills)
			if len(processSkills) == 0 {
				t.Fatalf("EmbeddedSkillsDir publish failure holds no skills in %q", processDir)
			}
		}

		skillProgramAssertExtractionFailures(t)
	})
}

// skillProgramDiscover returns the available skills Discover finds outside the
// bundled set, keyed by catalog name.
func skillProgramDiscover(t *testing.T, env execenv.ExecutionEnvironment, extraDirs ...string) map[string]SkillMeta {
	t.Helper()
	bundledDir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	out := map[string]SkillMeta{}
	for name, d := range Discover(env, DiscoverOptions{ExtraDirs: extraDirs}).Entries {
		if d.Unavailable || strings.HasPrefix(d.Meta.SkillFile, bundledDir+string(filepath.Separator)) {
			continue
		}
		out[name] = d.Meta
	}
	return out
}

func skillProgramLimit(s string) string {
	const maxBytes = 4 << 10
	if len(s) > maxBytes {
		return s[:maxBytes]
	}
	return s
}

func skillProgramDocument(name, description, body string, shape uint8) string {
	var allowed string
	switch shape % 4 {
	case 0:
		allowed = "allowed-tools:\n  - read_file\n  - edit_file\n"
	case 1:
		allowed = "allowed-tools: shell\n"
	case 2:
		allowed = "allowed-tools: grep\n"
	}
	return "---\nname: " + name + "\ndescription: " + description + "\n" + allowed + "---\n" + body
}

func skillProgramWrite(t *testing.T, root, dir, content string) string {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func skillProgramBody(t *testing.T, document string) string {
	t.Helper()
	parsed, err := frontmatter.Parse(document)
	if err != nil {
		t.Fatalf("fixture frontmatter: %v", err)
	}
	return parsed.Body
}

func skillProgramAssertDiscovered(t *testing.T, skills map[string]SkillMeta, name, document, expectedDir string) {
	t.Helper()
	meta, ok := skills[name]
	if !ok {
		t.Fatalf("missing discovered skill %q in %v", name, skillProgramNames(skills))
	}
	if meta.Name != name || meta.Dir != filepath.Join(expectedDir, name) || meta.SkillFile != filepath.Join(expectedDir, name, "SKILL.md") {
		t.Fatalf("discovered %q metadata = %#v", name, meta)
	}
	body, err := loadSkillBody(meta)
	if err != nil {
		t.Fatalf("loadSkillBody(%q): %v", name, err)
	}
	if want := skillProgramBody(t, document); body != want {
		t.Fatalf("loadSkillBody(%q) = %q, want %q", name, body, want)
	}
	if strings.TrimSpace(meta.Description) == "" {
		t.Fatalf("discovered %q blank description", name)
	}
}

func skillProgramNames(skills map[string]SkillMeta) []string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	return names
}

// skillProgramAssertExtractionFailures exercises failures at the filesystem
// boundary through the narrow dependency accepted by extractEmbeddedSkills.
// Every temporary directory is under t.TempDir, and the failure cases prove the
// extractor removes a partially created destination before returning.
func skillProgramAssertExtractionFailures(t *testing.T) {
	t.Helper()

	if _, err := extractEmbeddedSkills(fstest.MapFS{}, func(string, string) (string, error) {
		return "", errors.New("temp directory fault")
	}); err == nil {
		t.Fatal("extractEmbeddedSkills succeeded after MkdirTemp failure")
	}

	assertCleaned := func(label string, source fs.FS) {
		t.Helper()
		parent := t.TempDir()
		var dest string
		_, err := extractEmbeddedSkills(source, func(_, pattern string) (string, error) {
			var makeErr error
			dest, makeErr = os.MkdirTemp(parent, pattern)
			return dest, makeErr
		})
		if err == nil {
			t.Fatalf("extractEmbeddedSkills %s failure succeeded", label)
		}
		if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
			t.Fatalf("extractEmbeddedSkills %s left %q behind: %v", label, dest, statErr)
		}
	}

	assertCleaned("walk", skillProgramWalkFaultFS{})
	assertCleaned("read", skillProgramReadFaultFS{FS: fstest.MapFS{
		"SKILL.md": &fstest.MapFile{Data: []byte("embedded skill")},
	}})
}

type skillProgramWalkFaultFS struct{}

func (skillProgramWalkFaultFS) Open(string) (fs.File, error) {
	return nil, errors.New("walk fault")
}

type skillProgramReadFaultFS struct{ fs.FS }

func (skillProgramReadFaultFS) ReadFile(string) ([]byte, error) {
	return nil, errors.New("read fault")
}

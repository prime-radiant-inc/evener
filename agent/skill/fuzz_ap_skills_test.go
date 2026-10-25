package skill

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/frontmatter"
	"primeradiant.com/evener/fuzz/edgeseeds"
)

// FuzzApSkillFileParse drives the SKILL.md discovery path — ScanSkillsDir →
// parseSkillFile (frontmatter decode + required-field validation + the
// allowed-tools scalar-vs-list coercion) → Load.
// The whole skill package was fuzz-unreachable; only fixed-fixture unit tests
// exercised this parser, so arbitrary SKILL.md bytes never hit it.
//
// The fuzzer owns SKILL.md bytes; a t.TempDir skill directory is the only I/O.
//
// Oracles (beyond never-panic):
//   - preserved invariant: every discovered SkillMeta has a non-blank Name and
//     Description (parseSkillFile's acceptance contract), and the map key equals
//     the meta's Name (ScanSkillsDir keys by name).
//   - body agreement: Load on a discovered skill returns exactly the
//     frontmatter body.
//   - determinism: scanning the same bytes twice discovers the same skill set.
//
// SAFETY: pure parse over an in-TempDir file — no network, no spawn.
func FuzzApSkillFileParse(f *testing.F) {
	seeds := []string{
		"---\nname: tdd\ndescription: test first\nallowed-tools:\n  - read_file\n  - shell\n---\nbody text\n",
		"---\nname: a\ndescription: d\n---\n",
		"---\nname: only-name\n---\nno description\n",
		"---\ndescription: only-desc\n---\nno name\n",
		"---\nname: \"   \"\ndescription: blank name after trim\n---\n",
		"---\nname: a\ndescription: d\nallowed-tools: not-a-list\n---\n",
		"---\nname: a\ndescription: d\nallowed-tools:\n  - x\n  - 42\n  - true\n---\n",
		"no frontmatter at all",
		"",
		"---\n",
		"name: a\ndescription: d\n---\nbody\n",
		"---\nname: a\ndescription: LEGACY: fixture\nuser-invocable: false\n---\nbody\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, s := range edgeseeds.FrontmatterYAML() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, "the-skill")
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		out := map[string]SkillMeta{}
		ScanSkillsDir(dir, out)

		for key, meta := range out {
			if meta.Name == "" {
				t.Fatalf("discovered skill has empty Name")
			}
			if meta.Description == "" {
				t.Fatalf("discovered skill %q has empty Description", meta.Name)
			}
			if key != meta.Name {
				t.Fatalf("map key %q != meta.Name %q", key, meta.Name)
			}

			// Body agreement: Load must equal the frontmatter body.
			body, err := loadSkillBody(meta)
			if err != nil {
				t.Fatalf("Load on discovered skill %q errored: %v", meta.Name, err)
			}
			// Derive the expected body through the same recovery Load applies, or a
			// recovered delimiterless input would disagree on the body.
			bodySource, _ := recoverMissingOpeningDelimiter(data)
			_, expectedBody, framed := frontmatter.Split(string(bodySource))
			if !framed {
				t.Fatal("discovered skill has no complete frontmatter")
			}
			if body != expectedBody {
				t.Fatalf("Load body mismatch for %q:\n got=%q\n want=%q", meta.Name, body, expectedBody)
			}
		}

		// Determinism: a second scan discovers the same names.
		out2 := map[string]SkillMeta{}
		ScanSkillsDir(dir, out2)
		if len(out2) != len(out) {
			t.Fatalf("ScanSkillsDir non-deterministic: %d vs %d skills", len(out2), len(out))
		}
		for name := range out {
			if _, ok := out2[name]; !ok {
				t.Fatalf("ScanSkillsDir non-deterministic: %q missing on second scan", name)
			}
		}
	})
}

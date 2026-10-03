package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

const directoryLinkBytes = "---\nname: linked-probe\ndescription: fixture\n---\nLINKED_BYTES_3689\n"

func directoryLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func checkDirectoryLinkLoad(t *testing.T, d Descriptor, name, source, content string) {
	t.Helper()
	if d.CatalogName != name || d.Meta.Name != "linked-probe" || d.Meta.SkillFile != source || d.Meta.Dir != filepath.Dir(source) || d.Unavailable {
		t.Fatalf("descriptor=%+v want name=%s source=%s", d, name, source)
	}
	loaded, diagnostics, err := Load(d)
	digest := sha256.Sum256([]byte(content))
	if err != nil || len(diagnostics) != 0 || loaded.Digest != hex.EncodeToString(digest[:]) || loaded.Body != "LINKED_BYTES_3689\n" {
		t.Fatalf("loaded=%+v diagnostics=%+v err=%v", loaded, diagnostics, err)
	}
	rendered := Render(loaded)
	var document SkillDocument
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(rendered.Content, "<skill-context>\n"), "\n</skill-context>")), &document); err != nil {
		t.Fatal(err)
	}
	if document.Name != name || document.Source != source || document.BaseDirectory != filepath.Dir(source) || document.Instructions != "LINKED_BYTES_3689\n" {
		t.Fatalf("document=%+v", document)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != content {
		t.Fatalf("preserved bytes=%q err=%v", data, err)
	}
}

// Catches ignoring child directory links, with byte-identical supported layouts as controls.
func TestSkillDirectoryLinksPersonalRegression(t *testing.T) {
	t.Parallel()
	target := filepath.Dir(portableWriteSkill(t, t.TempDir(), "target", directoryLinkBytes))
	for _, layout := range []string{"real-directory", "file-link", "directory-link"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			child := filepath.Join(root, "installed-name")
			source := filepath.Join(child, "SKILL.md")
			switch layout {
			case "real-directory":
				portableWriteSkill(t, root, "installed-name", directoryLinkBytes)
			case "file-link":
				directoryLink(t, filepath.Join(target, "SKILL.md"), source)
			case "directory-link":
				directoryLink(t, target, child)
			}
			catalog := Discover(nil, DiscoverOptions{UserSkillsDir: root})
			d, err := catalog.ResolveExact("linked-probe")
			if err != nil {
				t.Fatalf("personal %s exact resolution: %v", layout, err)
			}
			checkDirectoryLinkLoad(t, d, "linked-probe", source, directoryLinkBytes)
		})
	}
}

// Catches differences among public roots and canonicalizing linked sources to their targets.
func TestSkillDirectoryLinksPublicRoots(t *testing.T) {
	t.Parallel()
	for _, category := range []string{"home", "user", "project-agents-root", "project-root", "project-agents-cwd", "project-cwd", "extra", "plugin"} {
		for _, relative := range []bool{false, true} {
			t.Run(category+map[bool]string{false: "/absolute", true: "/relative"}[relative], func(t *testing.T) {
				project := fixtureRoot(t)
				if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				cwd := filepath.Join(project, "nested")
				if err := os.Mkdir(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				home, user, extra, plugin := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
				opts := DiscoverOptions{HomeDir: home, UserSkillsDir: user, ExtraDirs: []string{extra}, Plugins: []PluginSource{{Name: "plug", Dir: plugin}}}
				roots := map[string]string{"home": filepath.Join(home, ".agents", "skills"), "user": user, "project-agents-root": filepath.Join(project, ".agents", "skills"), "project-root": filepath.Join(project, "skills"), "project-agents-cwd": filepath.Join(cwd, ".agents", "skills"), "project-cwd": filepath.Join(cwd, "skills"), "extra": extra, "plugin": filepath.Join(plugin, "skills")}
				root := roots[category]
				target := filepath.Dir(portableWriteSkill(t, t.TempDir(), "target", directoryLinkBytes))
				before, err := os.Stat(filepath.Join(target, "SKILL.md"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "collateral"), []byte("COLLATERAL_3689"), 0o644); err != nil {
					t.Fatal(err)
				}
				linkTarget := target
				if relative {
					var err error
					linkTarget, err = filepath.Rel(root, target)
					if err != nil {
						t.Fatal(err)
					}
				}
				link := filepath.Join(root, "installed-name")
				directoryLink(t, linkTarget, link)
				catalog := Discover(execenv.NewLocalExecutionEnvironment(cwd), opts)
				name := "linked-probe"
				if category == "plugin" {
					name = "plug:linked-probe"
				}
				d, err := catalog.ResolveExact(name)
				if err != nil {
					t.Fatal(err)
				}
				checkDirectoryLinkLoad(t, d, name, filepath.Join(link, "SKILL.md"), directoryLinkBytes)
				for _, view := range [][]Descriptor{catalog.ModelEntries(), catalog.UserEntries()} {
					if !slices.ContainsFunc(view, func(d Descriptor) bool { return d.CatalogName == name }) {
						t.Fatalf("not advertised: %s", name)
					}
				}
				for _, entry := range catalog.InspectionEntries() {
					if entry.Meta.Dir != "" || entry.Meta.SkillFile != "" {
						t.Fatalf("inspection paths=%+v", entry)
					}
				}
				collateral, err := os.ReadFile(filepath.Join(d.Meta.Dir, "collateral"))
				if err != nil || string(collateral) != "COLLATERAL_3689" {
					t.Fatalf("collateral=%q err=%v", collateral, err)
				}
				old := map[string]SkillMeta{}
				ScanSkillsDir(root, old)
				meta, ok := old["linked-probe"]
				if !ok || meta.SkillFile != filepath.Join(link, "SKILL.md") {
					t.Fatalf("older scanner=%+v", old)
				}
				body, err := LoadSkillBody(meta)
				if err != nil || body != "LINKED_BYTES_3689\n" {
					t.Fatalf("older body=%q err=%v", body, err)
				}
				stored, err := os.Readlink(link)
				if err != nil || stored != linkTarget {
					t.Fatalf("link changed=%q err=%v", stored, err)
				}
				after, err := os.Stat(filepath.Join(target, "SKILL.md"))
				if err != nil || after.Mode() != before.Mode() {
					t.Fatalf("source permissions changed: before=%v after=%v err=%v", before, after, err)
				}
			})
		}
	}
}

// Catches suppressing broken-link diagnostics, disabling siblings or retaining a stale load failure.
func TestSkillDirectoryLinksDiagnosticsAndRecovery(t *testing.T) {
	t.Parallel()
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Dir(portableWriteSkill(t, outside, "target", directoryLinkBytes))
	link := filepath.Join(root, "linked")
	directoryLink(t, target, link)
	healthy := portableWriteSkill(t, root, "healthy", "---\nname: healthy\ndescription: fixture\n---\nHEALTHY_3689\n")
	fileTarget := portableWriteSkill(t, outside, "file-target", "---\nname: file-healthy\ndescription: fixture\n---\nFILE_HEALTHY_3689\n")
	directoryLink(t, fileTarget, filepath.Join(root, "file-healthy", "SKILL.md"))
	broken := filepath.Join(root, "broken")
	directoryLink(t, filepath.Join(outside, "restored"), broken)
	loop := filepath.Join(root, "loop")
	directoryLink(t, "loop", loop)
	// ENOTDIR is a deterministic unreadable target on privileged and unprivileged hosts.
	blocker := filepath.Join(outside, "blocker")
	if err := os.WriteFile(blocker, []byte("BLOCKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(root, "unreadable")
	directoryLink(t, filepath.Join(blocker, "child"), unreadable)
	directoryLink(t, fileTarget, filepath.Join(root, "regular-file-link"))
	if err := os.WriteFile(filepath.Join(root, "regular-file"), []byte("NOT_A_SKILL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "collection")
	directoryLink(t, outside, nested)
	badSkill := filepath.Join(outside, "unreadable-skill", "SKILL.md")
	if err := os.MkdirAll(badSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	directoryLink(t, filepath.Dir(badSkill), filepath.Join(root, "unreadable-skill"))
	discover := func() Catalog { return Discover(nil, DiscoverOptions{UserSkillsDir: root}) }
	catalog := discover()
	for _, source := range []string{broken, loop, unreadable, filepath.Join(root, "unreadable-skill", "SKILL.md")} {
		if !slices.ContainsFunc(catalog.Diagnostics, func(d Diagnostic) bool {
			return d.Category == "unreadable_source" && d.Source == source && d.Name == ""
		}) {
			t.Fatalf("missing unreadable diagnostic at %s: %+v", source, catalog.Diagnostics)
		}
	}
	if len(catalog.Diagnostics) != 4 {
		t.Fatalf("unexpected diagnostics=%+v", catalog.Diagnostics)
	}
	retained, err := catalog.ResolveExact("linked-probe")
	if err != nil {
		t.Fatal(err)
	}
	if retained.Meta.SkillFile != filepath.Join(link, "SKILL.md") {
		t.Fatalf("nested scan displaced source: %+v", retained)
	}
	for _, name := range []string{"healthy", "file-healthy"} {
		d, err := catalog.ResolveExact(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(d); err != nil {
			t.Fatal(err)
		}
	}
	old := map[string]SkillMeta{}
	ScanSkillsDir(root, old)
	if len(old) != 3 {
		t.Fatalf("quiet scanner=%+v", old)
	}
	moved := filepath.Join(outside, "temporarily-away")
	if err := os.Rename(target, moved); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err := Load(retained)
	if err == nil || len(diagnostics) != 1 || diagnostics[0].Category != "unreadable_source" || diagnostics[0].Source != retained.Meta.SkillFile {
		t.Fatalf("missing load err=%v diagnostics=%+v", err, diagnostics)
	}
	if _, err := discover().ResolveExact("linked-probe"); err == nil {
		t.Fatal("missing target remained discoverable")
	}
	if err := os.Rename(moved, target); err != nil {
		t.Fatal(err)
	}
	checkDirectoryLinkLoad(t, retained, "linked-probe", filepath.Join(link, "SKILL.md"), directoryLinkBytes)
	if _, err := discover().ResolveExact("linked-probe"); err != nil {
		t.Fatal(err)
	}
	portableWriteSkill(t, outside, "restored", "---\nname: restored\ndescription: fixture\n---\nRESTORED_3689\n")
	recovered := discover()
	if _, err := recovered.ResolveExact("restored"); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(recovered.Diagnostics, func(d Diagnostic) bool { return d.Source == broken }) {
		t.Fatalf("stale broken diagnostic=%+v", recovered.Diagnostics)
	}
	updated := strings.Replace(directoryLinkBytes, "LINKED_BYTES_3689", "UPDATED_BYTES_3689", 1)
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(retained)
	updatedDigest := sha256.Sum256([]byte(updated))
	if err != nil || loaded.Body != "UPDATED_BYTES_3689\n" || loaded.Digest != hex.EncodeToString(updatedDigest[:]) {
		t.Fatalf("same-name current bytes=%+v err=%v", loaded, err)
	}
	changed := strings.Replace(directoryLinkBytes, "name: linked-probe", "name: changed-probe", 1)
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, err = Load(retained)
	if err == nil || !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "source_identity_changed" }) {
		t.Fatalf("identity err=%v diagnostics=%+v", err, diagnostics)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(directoryLinkBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	checkDirectoryLinkLoad(t, retained, "linked-probe", filepath.Join(link, "SKILL.md"), directoryLinkBytes)
	if data, err := os.ReadFile(healthy); err != nil || string(data) != "---\nname: healthy\ndescription: fixture\n---\nHEALTHY_3689\n" {
		t.Fatalf("sibling changed=%q err=%v", data, err)
	}
	if stored, err := os.Readlink(link); err != nil || stored != target {
		t.Fatalf("link changed=%q err=%v", stored, err)
	}
}

// Catches linked invalid winners exposing lower-precedence instructions or controls being dropped.
func TestSkillDirectoryLinksPrecedenceAndControls(t *testing.T) {
	t.Parallel()
	low, high, outside := t.TempDir(), t.TempDir(), t.TempDir()
	previous := portableWriteSkill(t, low, "probe", directoryLinkBytes)
	for _, f := range []struct{ name, controls string }{{"linked-probe", "user-invocable: \"true\"\n"}, {"user-only", "disable-model-invocation: true\n"}, {"model-only", "user-invocable: false\n"}, {"hidden", "disable-model-invocation: true\nuser-invocable: false\n"}} {
		source := portableWriteSkill(t, outside, f.name, "---\nname: "+f.name+"\ndescription: fixture\n"+f.controls+"---\nCONTROLS_3689\n")
		directoryLink(t, filepath.Dir(source), filepath.Join(high, f.name))
	}
	malformed := portableWriteSkill(t, outside, "malformed", "---\nname: [\n---\n")
	directoryLink(t, filepath.Dir(malformed), filepath.Join(high, "malformed"))
	catalog := Discover(nil, DiscoverOptions{ExtraDirs: []string{low, high}})
	winner, err := catalog.ResolveExact("linked-probe")
	if err != nil || !winner.Unavailable || winner.Meta.SkillFile != filepath.Join(high, "linked-probe", "SKILL.md") {
		t.Fatalf("winner=%+v err=%v", winner, err)
	}
	if !slices.ContainsFunc(catalog.Diagnostics, func(d Diagnostic) bool {
		return d.Category == "collision" && d.Source == winner.Meta.SkillFile && d.OtherSource == previous
	}) {
		t.Fatalf("collision=%+v", catalog.Diagnostics)
	}
	if !slices.ContainsFunc(catalog.Diagnostics, func(d Diagnostic) bool {
		return d.Category == "invalid_frontmatter" && d.Source == filepath.Join(high, "malformed", "SKILL.md")
	}) {
		t.Fatalf("malformed=%+v", catalog.Diagnostics)
	}
	for _, f := range []struct {
		name        string
		model, user bool
	}{{"linked-probe", false, false}, {"user-only", false, true}, {"model-only", true, false}, {"hidden", false, false}} {
		for _, view := range []struct {
			entries []Descriptor
			want    bool
		}{{catalog.ModelEntries(), f.model}, {catalog.UserEntries(), f.user}} {
			got := slices.ContainsFunc(view.entries, func(d Descriptor) bool { return d.CatalogName == f.name })
			if got != view.want {
				t.Fatalf("advertisement %s=%v want=%v", f.name, got, view.want)
			}
		}
	}
	old := map[string]SkillMeta{}
	ScanSkillsDir(high, old)
	if len(old) != 3 {
		t.Fatalf("legacy invalid skip=%+v", old)
	}
}

func TestSkillDirectoryLinksSymlinkedRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Dir(portableWriteSkill(t, t.TempDir(), "target", directoryLinkBytes))
	directoryLink(t, target, filepath.Join(root, "child"))
	configured := filepath.Join(t.TempDir(), "root-link")
	directoryLink(t, root, configured)
	d, err := Discover(nil, DiscoverOptions{ExtraDirs: []string{configured}}).ResolveExact("linked-probe")
	if err != nil {
		t.Fatal(err)
	}
	checkDirectoryLinkLoad(t, d, "linked-probe", filepath.Join(configured, "child", "SKILL.md"), directoryLinkBytes)
}

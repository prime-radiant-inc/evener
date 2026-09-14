package skill

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

func portableWriteSkill(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPortableProjectDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := portableWriteSkill(t, filepath.Join(root, ".agents", "skills"), "probe", "---\nname: probe\ndescription: fixture\n---\nPORTABLE_31ac\n")
	catalog := Discover(execenv.NewLocalExecutionEnvironment(root), DiscoverOptions{HomeDir: t.TempDir()})
	if got := catalog.Entries["probe"].Meta.SkillFile; got != want {
		t.Fatalf("source=%q want=%q", got, want)
	}
}

func TestSkillDiscoveryPrecedence(t *testing.T) {
	root, home, user, extra1, extra2, plug := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "nested")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	opts := DiscoverOptions{HomeDir: home, UserSkillsDir: user, ExtraDirs: []string{extra1, extra2}, Plugins: []PluginSource{{Name: "plug", Dir: plug}}}
	initial := Discover(env, opts)
	embedded, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatal(err)
	}
	name := "doctoring-evener"
	previous := filepath.Join(embedded, name, "SKILL.md")
	if got := initial.Entries[name].Meta.SkillFile; got != previous {
		t.Fatalf("embedded source=%q want=%q", got, previous)
	}
	roots := []string{filepath.Join(home, ".agents", "skills"), user, filepath.Join(root, ".agents", "skills"), filepath.Join(root, "skills"), filepath.Join(cwd, ".agents", "skills"), filepath.Join(cwd, "skills"), extra1, extra2}
	for _, dir := range roots {
		want := portableWriteSkill(t, dir, "fixture", "---\nname: "+name+"\ndescription: fixture\n---\nPRECEDENCE_1\n")
		c := Discover(env, opts)
		if got := c.Entries[name].Meta.SkillFile; got != want {
			t.Fatalf("winner=%q want=%q", got, want)
		}
		found := false
		for _, d := range c.Diagnostics {
			if d.Category == "collision" && d.Name == name && d.Source == want && d.OtherSource == previous {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing collision winner=%q displaced=%q: %+v", want, previous, c.Diagnostics)
		}
		previous = want
	}
	bare := portableWriteSkill(t, extra2, "qualified", "---\nname: plug:probe\ndescription: fixture\n---\nQUALIFIED_1\n")
	winner := portableWriteSkill(t, filepath.Join(plug, "skills"), "probe", "---\nname: probe\ndescription: fixture\nuser-invocable: \"true\"\n---\nINVALID_1\n")
	c := Discover(env, opts)
	d, err := c.ResolveExact("plug:probe")
	if err != nil || !d.Unavailable || d.Meta.Name != "probe" || d.Meta.SkillFile != winner {
		t.Fatalf("invalid plugin winner=%+v err=%v", d, err)
	}
	found := false
	for _, diag := range c.Diagnostics {
		if diag.Category == "collision" && diag.Source == winner && diag.OtherSource == bare {
			found = true
		}
	}
	if !found {
		t.Fatalf("qualified collision missing: %+v", c.Diagnostics)
	}
}

func TestSkillDiscoveryInvalidWinnerAndDiagnostics(t *testing.T) {
	low, high := t.TempDir(), t.TempDir()
	portableWriteSkill(t, low, "probe", "---\nname: probe\ndescription: fixture\n---\nLOW_1\n")
	want := portableWriteSkill(t, high, "probe", "---\nname: probe\ndescription: fixture\ndisable-model-invocation: \"false\"\n---\nHIGH_1\n")
	malformed := portableWriteSkill(t, high, "malformed", "---\nname: [\n---\n")
	unreadable := filepath.Join(high, "unreadable", "SKILL.md")
	if err := os.MkdirAll(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	notDirectory := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(notDirectory, []byte("ROOT_1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{low, high, missing, notDirectory}})
	d, err := c.Resolve("probe")
	if err != nil || !d.Unavailable || d.Meta.SkillFile != want {
		t.Fatalf("winner=%+v err=%v", d, err)
	}
	for _, view := range [][]Descriptor{c.ModelEntries(), c.UserEntries()} {
		for _, d := range view {
			if d.CatalogName == "probe" {
				t.Fatal("invalid winner advertised")
			}
		}
	}
	for source, category := range map[string]string{want: "invalid_control", malformed: "invalid_frontmatter", unreadable: "unreadable_source", missing: "unreadable_source", notDirectory: "unreadable_source"} {
		found := false
		for _, diag := range c.Diagnostics {
			if diag.Source == source && diag.Category == category {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s at %s: %+v", category, source, c.Diagnostics)
		}
	}
	clean := Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), UserSkillsDir: filepath.Join(t.TempDir(), "absent")})
	if len(clean.Diagnostics) != 0 {
		t.Fatalf("optional directories: %+v", clean.Diagnostics)
	}
}

func TestSkillCatalogResolutionAndViews(t *testing.T) {
	dir, one, two := t.TempDir(), t.TempDir(), t.TempDir()
	for _, f := range []struct{ name, controls string }{{"both", ""}, {"user", "disable-model-invocation: true\n"}, {"model", "user-invocable: false\n"}, {"neither", "disable-model-invocation: true\nuser-invocable: false\n"}} {
		portableWriteSkill(t, dir, f.name, "---\nname: "+f.name+"\ndescription: fixture\n"+f.controls+"---\nVIEW_1\n")
	}
	for _, root := range []string{one, two} {
		portableWriteSkill(t, filepath.Join(root, "skills"), "probe", "---\nname: probe\ndescription: fixture\n---\nRESOLVE_1\n")
	}
	portableWriteSkill(t, filepath.Join(one, "skills"), "unique", "---\nname: unique\ndescription: fixture\n---\nUNIQUE_1\n")
	c := Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{dir}, Plugins: []PluginSource{{Name: "one", Dir: one}, {Name: "two", Dir: two}}})
	d, err := c.Resolve("unique")
	if err != nil || d.CatalogName != "one:unique" || d.Meta.Name != "unique" {
		t.Fatalf("suffix=%+v err=%v", d, err)
	}
	for _, name := range []string{"missing", "unique"} {
		_, err := c.ResolveExact(name)
		var re *ResolutionError
		if !errors.As(err, &re) || re.Kind != "unknown" || re.Name != name {
			t.Fatalf("exact %s: %v", name, err)
		}
	}
	_, err = c.Resolve("probe")
	var re *ResolutionError
	if !errors.As(err, &re) || re.Kind != "ambiguous" || !reflect.DeepEqual(re.Candidates, []string{"one:probe", "two:probe"}) {
		t.Fatalf("ambiguity: %+v", err)
	}
	for _, kind := range []string{"model", "user"} {
		view := c.ModelEntries()
		want := []string{"both", "model"}
		if kind == "user" {
			view = c.UserEntries()
			want = []string{"both", "user"}
		}
		var got []string
		for _, d := range view {
			if d.Meta.Dir == filepath.Join(dir, d.Meta.Name) {
				got = append(got, d.CatalogName)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s view=%v want=%v", kind, got, want)
		}
	}
	portableWriteSkill(t, dir, "probe", "---\nname: probe\ndescription: fixture\n---\nEXACT_1\n")
	c = Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{dir}, Plugins: []PluginSource{{Name: "one", Dir: one}, {Name: "two", Dir: two}}})
	if d, err := c.Resolve("probe"); err != nil || d.CatalogName != "probe" {
		t.Fatalf("exact not preferred: %+v %v", d, err)
	}
	if _, err := c.Resolve("absent:probe"); !errors.As(err, &re) || re.Kind != "unknown" {
		t.Fatalf("qualified suffix matched: %v", err)
	}
}

func TestSkillCatalogInspectionCopiesNonStringKeyMetadata(t *testing.T) {
	dir := t.TempDir()
	portableWriteSkill(t, dir, "numeric", "---\nname: numeric\ndescription: fixture\nmetadata:\n  1:\n    - ORIGINAL\n    - true: [ORIGINAL]\n---\nBODY\n")
	catalog := Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{dir}})
	descriptor, err := catalog.ResolveExact("numeric")
	if err != nil || descriptor.Unavailable {
		t.Fatalf("parsed descriptor=%+v err=%v", descriptor, err)
	}
	want := map[any]any{1: []any{"ORIGINAL", map[any]any{true: []any{"ORIGINAL"}}}}
	if got := descriptor.Meta.Metadata["metadata"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered metadata=%#v want=%#v", got, want)
	}
	var inspection Descriptor
	for _, d := range catalog.InspectionEntries() {
		if d.CatalogName == "numeric" {
			inspection = d
		}
	}
	if got := inspection.Meta.Metadata["metadata"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("inspection metadata=%#v want=%#v", got, want)
	}
	nested := inspection.Meta.Metadata["metadata"].(map[any]any)
	nested[2] = "MUTATED"
	nested[1].([]any)[0] = "MUTATED"
	nested[1].([]any)[1].(map[any]any)[true].([]any)[0] = "MUTATED"
	if got := catalog.Entries["numeric"].Meta.Metadata["metadata"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("inspection mutation changed live metadata=%#v want=%#v", got, want)
	}
}

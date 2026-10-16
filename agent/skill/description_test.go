package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSkillDescriptionRecovery(t *testing.T) {
	for _, name := range []string{"local-dev", "sen"} {
		for _, framing := range []string{"LF", "CRLF", "BOM"} {
			t.Run(name+"/"+framing, func(t *testing.T) {
				description := "LEGACY (previous assistant): use " + name + " for this fixture"
				data := "---\nname: " + name + "\ndescription: " + description + "\nuser-invocable: false\ndisable-model-invocation: true\n---\nBODY_472\n"
				switch framing {
				case "CRLF":
					data = strings.ReplaceAll(data, "\n", "\r\n")
				case "BOM":
					data = "\ufeff" + data
				}
				path := filepath.Join(t.TempDir(), "SKILL.md")
				d, diagnostics, err := Parse([]byte(data), path)
				if err != nil || d.Unavailable {
					t.Fatalf("Parse unavailable=%v err=%v diagnostics=%+v", d.Unavailable, err, diagnostics)
				}
				if d.CatalogName != name || d.Meta.Name != name || d.Meta.SkillFile != path || d.Meta.Description != description {
					t.Fatalf("identity or description changed: %+v", d)
				}
				if d.Controls.UserInvocable || !d.Controls.DisableModelInvocation {
					t.Fatalf("controls changed: %+v", d.Controls)
				}
				if len(diagnostics) != 1 || diagnostics[0].Category != "unquoted_description" || diagnostics[0].Field != "description" || diagnostics[0].Name != name || diagnostics[0].Source != path {
					t.Fatalf("recovery diagnostic=%+v", diagnostics)
				}
			})
		}
	}
}

func TestSkillDescriptionSyntax(t *testing.T) {
	tests := []struct {
		name, block, description string
		recovered, invalid       bool
	}{
		{name: "plain", block: "description: plain prose", description: "plain prose"},
		{name: "quoted", block: "description: 'prose: quoted'", description: "prose: quoted"},
		{name: "double quoted", block: "description: \"prose: quoted\"", description: "prose: quoted"},
		{name: "literal", block: "description: |\n  prose: literal", description: "prose: literal\n"},
		{name: "folded", block: "description: >-\n  prose: folded\n  continuation", description: "prose: folded continuation"},
		{name: "valid comment", block: "description: plain # prose: comment", description: "plain"},
		{name: "valid nested", block: "description: plain\nmetadata:\n  description: nested\n  list: [one, two]", description: "plain"},
		{name: "comment", block: "description: prose: recovered # comment: ignored", description: "prose: recovered", recovered: true},
		{name: "hash", block: "description: prose: issue#12", description: "prose: issue#12", recovered: true},
		{name: "embedded quotes", block: "description: prose: use \"quoted\" and 'single'", description: "prose: use \"quoted\" and 'single'", recovered: true},
		{name: "terminal document end", block: "description: prose: recovered\n...\n# final comment", description: "prose: recovered", recovered: true},
		{name: "unclosed quote", block: "description: \"prose: broken", invalid: true},
		{name: "unclosed single quote", block: "description: 'prose: broken", invalid: true},
		{name: "sequence", block: "description: [prose: broken", invalid: true},
		{name: "mapping", block: "description: {prose: broken", invalid: true},
		{name: "tag", block: "description: !!str prose: broken", invalid: true},
		{name: "anchor", block: "description: &anchor prose: broken", invalid: true},
		{name: "alias", block: "description: *alias prose: broken", invalid: true},
		{name: "block indicator", block: "description: | prose: broken", invalid: true},
		{name: "duplicate description", block: "description: prose: broken\ndescription: other", invalid: true},
		{name: "duplicate control", block: "description: prose: broken\nuser-invocable: false\nuser-invocable: true", invalid: true},
		{name: "nested description", block: "description: valid\nmetadata:\n  description: prose: broken", invalid: true},
		{name: "continuation", block: "description: prose: broken\n  continuation", invalid: true},
		{name: "continuation after comment", block: "description: prose: broken\n# comment\n\n  continuation", invalid: true},
		{name: "unrelated YAML", block: "description: prose: broken\nother: [", invalid: true},
		{name: "invalid encoding", block: "description: prose: \xff", invalid: true},
		{name: "control character", block: "description: prose: \x00", invalid: true},
		{name: "unicode line break", block: "description: prose: first\u2028  continuation", invalid: true},
		{name: "invalid boolean", block: "description: prose: broken\nuser-invocable: 'false'", invalid: true},
		{name: "invalid model boolean", block: "description: prose: broken\ndisable-model-invocation: 'true'", invalid: true},
		{name: "trailing control", block: "description: prose: broken\n...\nuser-invocable: false", invalid: true},
		{name: "trailing garbage", block: "description: prose: broken\n...\n[", invalid: true},
		{name: "second document", block: "description: prose: broken\n...\n--- # another document\nuser-invocable: false", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("---\nname: fixture\n" + tt.block + "\n---\nBODY_472\n")
			d, diagnostics, err := Parse(data, filepath.Join(t.TempDir(), "SKILL.md"))
			if tt.invalid {
				if err == nil || !d.Unavailable {
					t.Fatalf("invalid source accepted: %+v err=%v", d, err)
				}
				return
			}
			if err != nil || d.Unavailable || d.Meta.Description != tt.description {
				t.Fatalf("description=%q want=%q unavailable=%v err=%v", d.Meta.Description, tt.description, d.Unavailable, err)
			}
			recovered := slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "unquoted_description" })
			if recovered != tt.recovered {
				t.Fatalf("recovered=%v want=%v diagnostics=%+v", recovered, tt.recovered, diagnostics)
			}
		})
	}
}

func TestSkillDescriptionRecoveryMappingBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, block string
		valid       bool
	}{
		{"flow controls", "{\nname: fixture,\ndescription: LEGACY: fixture, user-invocable: false, disable-model-invocation: true\n}", false},
		{"flow invalid control", "{\nname: fixture,\ndescription: LEGACY: fixture, user-invocable: 'false'\n}", false},
		{"flow invalid tools", "{\nname: fixture,\ndescription: LEGACY: fixture, allowed-tools: [true]\n}", false},
		{"nested flow description", "name: fixture\n'description': top-level prose\nmetadata: {\ndescription: LEGACY: nested prose\n}", false},
		{"valid flow controls", "{\nname: fixture,\ndescription: 'LEGACY: fixture', user-invocable: false, disable-model-invocation: true\n}", true},
		{"alias duplicate user control", "name: fixture\ndescription: LEGACY: fixture\nmetadata: &key user-invocable\n*key: 'false'\nuser-invocable: true", false},
		{"alias duplicate model control", "name: fixture\ndescription: LEGACY: fixture\nmetadata: &key disable-model-invocation\n*key: 'true'\ndisable-model-invocation: false", false},
		{"alias duplicate tools", "name: fixture\ndescription: LEGACY: fixture\nmetadata: &key allowed-tools\n*key: [true]\nallowed-tools: [read_file]", false},
		{"anchored duplicate key", "name: fixture\ndescription: LEGACY: fixture\n&key user-invocable: 'false'\n*key: true", false},
		{"nested alias duplicate", "name: fixture\ndescription: LEGACY: fixture\nmetadata:\n  key: &key nested\n  *key: one\n  nested: two", false},
		{"valid alias controls", "name: fixture\ndescription: LEGACY: fixture\nmetadata: {user-key: &user user-invocable, model-key: &model disable-model-invocation}\n*user: false\n*model: true", true},
		{"encoded duplicate control", "name: fixture\ndescription: LEGACY: fixture\n!!binary dXNlci1pbnZvY2FibGU=: 'false'\nuser-invocable: true", false},
		{"encoded alias duplicate control", "name: fixture\ndescription: LEGACY: fixture\nmetadata: &key !!binary dXNlci1pbnZvY2FibGU=\n*key: 'false'\nuser-invocable: true", false},
		{"nested encoded duplicate", "name: fixture\ndescription: LEGACY: fixture\nmetadata:\n  !!binary bmVzdGVk: one\n  nested: two", false},
		{"nested numeric duplicate", "name: fixture\ndescription: LEGACY: fixture\nmetadata: {0x1: one, 1: two}", false},
		{"distinct nested keys", "name: fixture\ndescription: LEGACY: fixture\nuser-invocable: false\ndisable-model-invocation: true\nmetadata: {null: one, '': two}", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := "---\n" + tt.block + "\n---\nBODY_472\n"
			path := portableWriteSkill(t, dir, "fixture", source)
			d, _, err := Parse([]byte(source), path)
			if (err == nil) != tt.valid || d.Unavailable == tt.valid {
				t.Errorf("Parse valid=%v unavailable=%v err=%v", tt.valid, d.Unavailable, err)
			}
			loaded, _, err := Load(d)
			if (err == nil) != tt.valid {
				t.Errorf("Load valid=%v err=%v", tt.valid, err)
			}
			if tt.valid && (d.Controls.UserInvocable || !d.Controls.DisableModelInvocation || loaded.Descriptor.Controls != d.Controls) {
				t.Errorf("controls changed: parsed=%+v loaded=%+v", d.Controls, loaded.Descriptor.Controls)
			}
			catalog := Discover(nil, DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{dir}})
			isFixture := func(d Descriptor) bool { return d.Meta.SkillFile == path }
			if slices.ContainsFunc(catalog.ModelEntries(), isFixture) || slices.ContainsFunc(catalog.UserEntries(), isFixture) {
				t.Error("restricted or invalid skill advertised")
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != source {
				t.Fatalf("source rewritten: %q err=%v", unchanged, err)
			}
		})
	}
}

func TestSkillDescriptionRecoveryPreservesAliasMergeKey(t *testing.T) {
	t.Parallel()
	source := "---\nname: fixture\ndescription: LEGACY: fixture\nmetadata: &key <<\n*key: {user-invocable: false, disable-model-invocation: true}\n---\nBODY_472\n"
	d, _, err := Parse([]byte(source), filepath.Join(t.TempDir(), "SKILL.md"))
	if err != nil || d.Unavailable {
		t.Fatalf("unavailable=%v err=%v", d.Unavailable, err)
	}
	if !d.Controls.UserInvocable || d.Controls.DisableModelInvocation {
		t.Fatalf("alias key interpreted as a merge: %+v", d.Controls)
	}
}

func TestSkillDescriptionDiscoveryLoadAndRepair(t *testing.T) {
	dir := t.TempDir()
	body := "# Fixture\n\nFIRST_472\n---\nLAST_923\n"
	source := "\ufeff" + strings.ReplaceAll("---\nname: recovered\ndescription: LEGACY: fixture\nuser-invocable: false\n---\n"+body, "\n", "\r\n")
	path := portableWriteSkill(t, dir, "recovered", source)
	portableWriteSkill(t, dir, "healthy", "---\nname: healthy\ndescription: healthy fixture\n---\nHEALTHY_23\n")
	broken := portableWriteSkill(t, dir, "broken", "---\nname: broken\ndescription: [\n---\nBROKEN_34\n")
	opts := DiscoverOptions{HomeDir: t.TempDir(), ExtraDirs: []string{dir}}
	catalog := Discover(nil, opts)
	d, err := catalog.ResolveExact("recovered")
	if err != nil || d.Unavailable {
		t.Fatalf("recovered skill unavailable: %+v err=%v diagnostics=%+v", d, err, catalog.Diagnostics)
	}
	healthy, err := catalog.ResolveExact("healthy")
	if err != nil || healthy.Unavailable {
		t.Fatalf("healthy sibling unavailable: %+v err=%v", healthy, err)
	}
	if d, err := catalog.ResolveExact("broken"); err == nil && !d.Unavailable {
		t.Fatal("broken sibling available")
	}
	for _, d := range catalog.UserEntries() {
		if d.CatalogName == "recovered" {
			t.Fatal("user-invocable false advertised")
		}
	}
	loaded, _, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(source))
	if loaded.Body != body || loaded.Digest != hex.EncodeToString(digest[:]) || loaded.Descriptor.Controls.UserInvocable {
		t.Fatalf("loaded source changed: %+v", loaded)
	}
	rendered := Render(loaded)
	encoded := strings.TrimSuffix(strings.TrimPrefix(rendered.Content, "<skill-context>\n"), "\n</skill-context>")
	var document SkillDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatal(err)
	}
	want := SkillDocument{Name: "recovered", Description: "LEGACY: fixture", Source: path, BaseDirectory: filepath.Dir(path), Instructions: body}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("rendered document=%+v want=%+v", document, want)
	}
	if err := os.WriteFile(broken, []byte("---\nname: broken\ndescription: repaired fixture\n---\nBROKEN_34\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repaired, err := Discover(nil, opts).ResolveExact("broken")
	if err != nil || repaired.Unavailable {
		t.Fatalf("repaired source unavailable: %+v err=%v", repaired, err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != source {
		t.Fatalf("source rewritten: %q err=%v", unchanged, err)
	}
}

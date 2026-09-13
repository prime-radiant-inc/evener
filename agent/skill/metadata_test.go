package skill

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSkillControlsParseSkillFileRejectsQuotedBoolean(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "probe", "---\nname: probe\ndescription: fixture\nuser-invocable: \"false\"\n---\nBODY_941\n")

	if descriptor, ok := parseSkillFile(path); ok {
		t.Fatalf("parseSkillFile(%q) = %+v, true; want invalid control rejection", filepath.Base(path), descriptor)
	}
}

func TestSkillControlsRejectQuotedBoolean(t *testing.T) {
	raw := []byte("---\nname: probe\ndescription: fixture\nuser-invocable: \"false\"\n---\nBODY_941\n")
	d, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !d.Unavailable || d.CatalogName != "probe" {
		t.Fatalf("descriptor=%+v error=%v", d, err)
	}
	found := false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Category == "invalid_control" && diagnostic.Field == "user-invocable"
	}
	if !found {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestSkillControlsStrictBooleanValues(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		valid       bool
		wantDisable bool
		wantUser    bool
	}{
		{name: "omitted", valid: true, wantUser: true},
		{name: "true", value: "true", valid: true},
		{name: "false", value: "false", valid: true},
		{name: "quoted true", value: `"true"`},
		{name: "quoted false", value: `"false"`},
		{name: "yes", value: "yes"},
		{name: "number", value: "1"},
		{name: "null", value: "null"},
		{name: "sequence", value: "[true]"},
		{name: "map", value: "{enabled: true}"},
	}
	fields := []string{"disable-model-invocation", "user-invocable"}
	for _, field := range fields {
		for _, tt := range tests {
			t.Run(field+"/"+tt.name, func(t *testing.T) {
				raw := "---\nname: probe\ndescription: fixture\n"
				if tt.value != "" {
					raw += field + ": " + tt.value + "\n"
				}
				raw += "---\nBODY_941\n"

				descriptor, diagnostics, err := Parse([]byte(raw), filepath.Join(t.TempDir(), "SKILL.md"))
				if !tt.valid {
					if err == nil || !descriptor.Unavailable || descriptor.CatalogName != "probe" || descriptor.Meta.Name != "probe" {
						t.Fatalf("descriptor=%+v error=%v", descriptor, err)
					}
					assertDiagnostic(t, diagnostics, "invalid_control", field)
					return
				}
				if err != nil || descriptor.Unavailable {
					t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
				}
				wantDisable, wantUser := tt.wantDisable, tt.wantUser
				if tt.value != "" {
					value := tt.value == "true"
					if field == "disable-model-invocation" {
						wantDisable = value
						wantUser = true
					} else {
						wantDisable = false
						wantUser = value
					}
				}
				if descriptor.Controls.DisableModelInvocation != wantDisable || descriptor.Controls.UserInvocable != wantUser {
					t.Fatalf("controls=%+v, want disable=%v user=%v", descriptor.Controls, wantDisable, wantUser)
				}
			})
		}
	}
}

func TestSkillControlsPreserveValidControlWhenOtherIsInvalid(t *testing.T) {
	raw := []byte("---\nname: probe\ndescription: fixture\ndisable-model-invocation: true\nuser-invocable: null\n---\nBODY_941\n")
	descriptor, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !descriptor.Unavailable || !descriptor.Controls.DisableModelInvocation {
		t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
	}
	assertDiagnostic(t, diagnostics, "invalid_control", "user-invocable")
}

func TestSkillControlsValidateRequiredMetadata(t *testing.T) {
	tests := []struct {
		name        string
		metadata    string
		field       string
		catalogName string
	}{
		{name: "missing name", metadata: "description: fixture\n", field: "name"},
		{name: "blank name", metadata: "name: \"  \"\ndescription: fixture\n", field: "name"},
		{name: "non-string name", metadata: "name: 7\ndescription: fixture\n", field: "name"},
		{name: "missing description", metadata: "name: probe\n", field: "description", catalogName: "probe"},
		{name: "blank description", metadata: "name: probe\ndescription: \"  \"\n", field: "description", catalogName: "probe"},
		{name: "non-string description", metadata: "name: probe\ndescription: [fixture]\n", field: "description", catalogName: "probe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor, diagnostics, err := Parse([]byte("---\n"+tt.metadata+"---\nBODY_941\n"), filepath.Join(t.TempDir(), "SKILL.md"))
			if err == nil || !descriptor.Unavailable || descriptor.CatalogName != tt.catalogName {
				t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
			}
			assertDiagnostic(t, diagnostics, "invalid_metadata", tt.field)
			if tt.catalogName != "" && descriptor.Meta.Name != tt.catalogName {
				t.Fatalf("declared name not preserved: %+v", descriptor)
			}
		})
	}
}

func TestSkillControlsRejectInvalidNames(t *testing.T) {
	for _, name := range []string{"-probe", "probe name", "plugin:one:probe"} {
		t.Run(name, func(t *testing.T) {
			raw := []byte("---\nname: \"" + name + "\"\ndescription: fixture\n---\nBODY_941\n")
			descriptor, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
			if err == nil || !descriptor.Unavailable || descriptor.CatalogName != "" {
				t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
			}
			assertDiagnostic(t, diagnostics, "invalid_metadata", "name")
		})
	}
}

func TestSkillControlsRejectInvalidFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing frontmatter", raw: "BODY_941\n"},
		{name: "unclosed", raw: "---\nname: probe\ndescription: fixture\nBODY_941\n"},
		{name: "closed malformed yaml", raw: "---\nname: [probe\ndescription: fixture\n---\nBODY_941\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor, diagnostics, err := Parse([]byte(tt.raw), filepath.Join(t.TempDir(), "SKILL.md"))
			if err == nil || !descriptor.Unavailable {
				t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
			}
			assertDiagnostic(t, diagnostics, "invalid_frontmatter", "")
		})
	}
}

func TestSkillControlsAllowedToolsFormsAndDiagnostic(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "string", value: "read_file", want: []string{"read_file"}},
		{name: "array", value: "[read_file, edit_file]", want: []string{"read_file", "edit_file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("---\nname: probe\ndescription: fixture\nallowed-tools: " + tt.value + "\n---\nBODY_941\n")
			descriptor, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
			if err != nil || descriptor.Unavailable || !reflect.DeepEqual(descriptor.Meta.AllowedTools, tt.want) {
				t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
			}
			assertDiagnostic(t, diagnostics, "allowed_tools_not_enforced", "allowed-tools")
		})
	}
}

func TestSkillControlsRejectMixedAllowedToolsArray(t *testing.T) {
	raw := []byte("---\nname: probe\ndescription: fixture\nallowed-tools: [read_file, 7]\n---\nBODY_941\n")
	descriptor, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !descriptor.Unavailable || descriptor.CatalogName != "probe" {
		t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
	}
	assertDiagnostic(t, diagnostics, "invalid_metadata", "allowed-tools")
}

func TestSkillControlsDiagnoseUnsupportedContextWithoutActing(t *testing.T) {
	raw := []byte("---\nname: probe\ndescription: fixture\ncontext: fork\n---\nBODY_941\n")
	descriptor, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
	if err != nil || descriptor.Unavailable {
		t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
	}
	assertDiagnostic(t, diagnostics, "unsupported_control", "context")
	if descriptor.Meta.Metadata["context"] != "fork" {
		t.Fatalf("metadata=%+v", descriptor.Meta.Metadata)
	}
}

func TestSkillControlsPreserveMetadataAndAbsoluteSource(t *testing.T) {
	relative := filepath.Join("relative", "probe", "SKILL.md")
	raw := []byte("---\nname: probe\ndescription: fixture\ntags: [one, two]\noptions:\n  nested: true\n---\nBODY_941\n")
	descriptor, diagnostics, err := Parse(raw, relative)
	if err != nil || descriptor.Unavailable || len(diagnostics) != 0 {
		t.Fatalf("descriptor=%+v diagnostics=%+v error=%v", descriptor, diagnostics, err)
	}
	if !filepath.IsAbs(descriptor.Meta.SkillFile) || descriptor.Meta.Dir != filepath.Dir(descriptor.Meta.SkillFile) {
		t.Fatalf("paths are not absolute and paired: %+v", descriptor.Meta)
	}
	if !reflect.DeepEqual(descriptor.Meta.Metadata["tags"], []any{"one", "two"}) {
		t.Fatalf("metadata=%+v", descriptor.Meta.Metadata)
	}
}

func TestSkillControlsCatalogEntriesDeepCopyMetadata(t *testing.T) {
	source := map[string]SkillMeta{
		"probe": {
			Name:         "declared",
			Description:  "fixture",
			AllowedTools: []string{"read_file"},
			Metadata: map[string]any{
				"tags":   []any{"one", map[string]any{"nested": "value"}},
				"config": map[string]any{"enabled": true},
			},
			Dir:       "/source/probe",
			SkillFile: "/source/probe/SKILL.md",
		},
	}

	entries := CatalogEntries(source)
	if len(entries) != 1 || entries[0].Name != "probe" || entries[0].Dir != "" || entries[0].SkillFile != "" {
		t.Fatalf("entries=%+v", entries)
	}
	entries[0].AllowedTools[0] = "changed"
	entries[0].Metadata["config"].(map[string]any)["enabled"] = false
	entries[0].Metadata["tags"].([]any)[1].(map[string]any)["nested"] = "changed"
	if source["probe"].AllowedTools[0] != "read_file" || source["probe"].Metadata["config"].(map[string]any)["enabled"] != true || source["probe"].Metadata["tags"].([]any)[1].(map[string]any)["nested"] != "value" {
		t.Fatalf("CatalogEntries aliased source metadata: %+v", source["probe"])
	}
}

func assertDiagnostic(t *testing.T, diagnostics []Diagnostic, category, field string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Category == category && diagnostic.Field == field {
			if diagnostic.Source == "" || !filepath.IsAbs(diagnostic.Source) {
				t.Fatalf("diagnostic source is not absolute: %+v", diagnostic)
			}
			return
		}
	}
	t.Fatalf("missing diagnostic category=%q field=%q in %+v", category, field, diagnostics)
}

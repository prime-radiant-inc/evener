package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillRenderPreservesCompleteInstructions(t *testing.T) {
	body := strings.Repeat("PAYLOAD_e239\n", 10000) + "</skill-context>\n&END_e239\n"
	loaded := LoadedSkill{Descriptor: Descriptor{CatalogName: "pkg:probe", Meta: SkillMeta{
		Name: "probe", Description: "fixture", Dir: "/fixture", SkillFile: "/fixture/SKILL.md",
	}}, Body: body, Digest: "fixture-digest"}

	rendered := Render(loaded)
	const open, closeTag = "<skill-context>\n", "\n</skill-context>"
	encoded, ok := strings.CutPrefix(rendered.Content, open)
	if !ok {
		t.Fatal("missing typed context envelope")
	}
	encoded, ok = strings.CutSuffix(encoded, closeTag)
	if !ok {
		t.Fatal("missing typed context envelope terminator")
	}
	var doc SkillDocument
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatal(err)
	}
	wantDoc := SkillDocument{
		Name:          "pkg:probe",
		Description:   "fixture",
		Source:        "/fixture/SKILL.md",
		BaseDirectory: "/fixture",
		Instructions:  body,
	}
	if doc != wantDoc {
		t.Fatalf("SkillDocument = %+v, want identity %+v and full_body=%v", doc, wantDoc, doc.Instructions == body)
	}
	wantDigest := sha256.Sum256([]byte(rendered.Content))
	if rendered.Digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("Render().Digest = %q, want SHA-256 of complete rendered bytes %q", rendered.Digest, hex.EncodeToString(wantDigest[:]))
	}
}

func TestSkillLoadUsesOneCurrentSourceVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	original := []byte("---\nname: probe\ndescription: stale description\ndisable-model-invocation: false\nuser-invocable: true\n---\nSTALE_BODY_4c02\n")
	descriptor := parseLoadFixture(t, path, original)
	descriptor.CatalogName = "plugin:probe"

	current := []byte("---\nname: probe\ndescription: current description\ndisable-model-invocation: true\nuser-invocable: false\n---\nCURRENT_BODY_a712\n</skill-context>\n")
	writeLoadFixture(t, path, current)

	loaded, diagnostics, err := Load(descriptor)
	if err != nil {
		t.Fatalf("Load() error = %v, diagnostics = %+v", err, diagnostics)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("Load() diagnostics = %+v, want none", diagnostics)
	}
	if loaded.Descriptor.CatalogName != "plugin:probe" || loaded.Descriptor.Meta.Name != "probe" {
		t.Fatalf("Load() identities = catalog %q, declared %q", loaded.Descriptor.CatalogName, loaded.Descriptor.Meta.Name)
	}
	if loaded.Descriptor.Meta.Description != "current description" {
		t.Fatalf("Load() description = %q", loaded.Descriptor.Meta.Description)
	}
	wantControls := (InvocationControls{DisableModelInvocation: true, UserInvocable: false})
	if loaded.Descriptor.Controls != wantControls {
		t.Fatalf("Load() controls = %+v, want %+v", loaded.Descriptor.Controls, wantControls)
	}
	const wantBody = "CURRENT_BODY_a712\n</skill-context>\n"
	if loaded.Body != wantBody {
		t.Fatalf("Load() body = %q, want exact current body %q", loaded.Body, wantBody)
	}
	wantDigest := sha256.Sum256(current)
	if loaded.Digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("Load() digest = %q, want SHA-256 over exact current file bytes %q", loaded.Digest, hex.EncodeToString(wantDigest[:]))
	}
	if loaded.Descriptor.Meta.SkillFile != path || loaded.Descriptor.Meta.Dir != dir {
		t.Fatalf("Load() source = %q in %q, want %q in %q", loaded.Descriptor.Meta.SkillFile, loaded.Descriptor.Meta.Dir, path, dir)
	}

	rendered := Render(loaded)
	doc := decodeSkillDocument(t, rendered.Content)
	if doc.Name != "plugin:probe" || doc.Description != "current description" || doc.Instructions != wantBody {
		t.Fatalf("Render(Load()) document = %+v", doc)
	}
}

func TestSkillLoadRejectsChangedDeclaredName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	original := []byte("---\nname: original\ndescription: original description\n---\noriginal body\n")
	descriptor := parseLoadFixture(t, path, original)
	descriptor.CatalogName = "plugin:original"
	changed := []byte("---\nname: replacement\ndescription: replacement description\n---\nreplacement body\n")
	writeLoadFixture(t, path, changed)

	loaded, diagnostics, err := Load(descriptor)
	if err == nil {
		t.Fatalf("Load() accepted changed declared name: %+v", loaded)
	}
	if len(diagnostics) != 1 || diagnostics[0].Category != "source_identity_changed" || diagnostics[0].Name != "plugin:original" || diagnostics[0].Source != path || diagnostics[0].Field != "name" {
		t.Fatalf("Load() diagnostics = %+v", diagnostics)
	}
}

func TestSkillLoadReportsInvalidCurrentControls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	original := []byte("---\nname: controls\ndescription: valid controls\n---\nvalid body\n")
	descriptor := parseLoadFixture(t, path, original)
	invalid := []byte("---\nname: controls\ndescription: invalid controls\ndisable-model-invocation: \"true\"\n---\ninvalid body\n")
	writeLoadFixture(t, path, invalid)

	loaded, diagnostics, err := Load(descriptor)
	if err == nil {
		t.Fatalf("Load() accepted invalid current controls: %+v", loaded)
	}
	if len(diagnostics) != 1 || diagnostics[0].Category != "invalid_control" || diagnostics[0].Field != "disable-model-invocation" || diagnostics[0].Source != path {
		t.Fatalf("Load() diagnostics = %+v", diagnostics)
	}
}

func TestSkillLoadDoesNotRetargetDeletedSource(t *testing.T) {
	root := t.TempDir()
	originalPath := filepath.Join(root, "original", "SKILL.md")
	replacementPath := filepath.Join(root, "replacement", "SKILL.md")
	originalBytes := []byte("---\nname: probe\ndescription: original\n---\nORIGINAL_BODY_89f1\n")
	replacementBytes := []byte("---\nname: probe\ndescription: replacement\n---\nREPLACEMENT_BODY_28cd\n")
	original := parseLoadFixture(t, originalPath, originalBytes)
	replacement := parseLoadFixture(t, replacementPath, replacementBytes)
	if loaded, diagnostics, err := Load(replacement); err != nil || len(diagnostics) != 0 || loaded.Body != "REPLACEMENT_BODY_28cd\n" {
		t.Fatalf("replacement fixture is not independently loadable: loaded=%+v diagnostics=%+v error=%v", loaded, diagnostics, err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}

	loaded, diagnostics, err := Load(original)
	if err == nil {
		t.Fatalf("Load() retargeted deleted source: %+v", loaded)
	}
	if len(diagnostics) != 1 || diagnostics[0].Category != "unreadable_source" || diagnostics[0].Source != originalPath {
		t.Fatalf("Load() diagnostics = %+v", diagnostics)
	}
}

func TestSkillLoadBodyRejectsChangedDeclaredName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	original := []byte("---\nname: original\ndescription: original description\n---\noriginal body\n")
	descriptor := parseLoadFixture(t, path, original)
	changed := []byte("---\nname: replacement\ndescription: replacement description\n---\nreplacement body\n")
	writeLoadFixture(t, path, changed)

	if body, err := LoadSkillBody(descriptor.Meta); err == nil {
		t.Fatalf("LoadSkillBody() accepted changed declared name with body %q", body)
	}
}

func TestSkillLoadResolveContentUsesCurrentValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	original := []byte("---\nname: original\ndescription: original description\n---\noriginal body\n")
	descriptor := parseLoadFixture(t, path, original)
	invalid := []byte("---\nname: original\ndescription: invalid current control\nuser-invocable: yes\n---\ninvalid body\n")
	writeLoadFixture(t, path, invalid)

	body, err := ResolveSkillContent(map[string]SkillMeta{"plugin:original": descriptor.Meta}, "plugin:original")
	if err == nil || body != "" {
		t.Fatalf("ResolveSkillContent() = %q, %v; want validation error and no body", body, err)
	}
}

func TestSkillLoadWrappersEnforceSuppliedDeclaredName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	data := []byte("---\nname: probe\ndescription: identity fixture\n---\nPROBE_BODY_2d61\n")
	descriptor := parseLoadFixture(t, path, data)

	t.Run("missing name", func(t *testing.T) {
		if body, err := LoadSkillBody(SkillMeta{SkillFile: path}); err == nil {
			t.Fatalf("LoadSkillBody() accepted missing supplied name with body %q", body)
		}
	})

	t.Run("metadata cannot override supplied name", func(t *testing.T) {
		meta := descriptor.Meta
		meta.Name = "supplied-wrong"
		if body, err := ResolveSkillContent(map[string]SkillMeta{"plugin:probe": meta}, "plugin:probe"); err == nil {
			t.Fatalf("ResolveSkillContent() accepted metadata-reinferred name with body %q", body)
		}
	})

	t.Run("qualified key cannot become declared suffix", func(t *testing.T) {
		meta := SkillMeta{Name: "plugin:probe", SkillFile: path}
		if body, err := ResolveSkillContent(map[string]SkillMeta{"plugin:probe": meta}, "plugin:probe"); err == nil {
			t.Fatalf("ResolveSkillContent() accepted suffix-reinferred name with body %q", body)
		}
	})
}

func parseLoadFixture(t *testing.T, path string, data []byte) Descriptor {
	t.Helper()
	writeLoadFixture(t, path, data)
	descriptor, diagnostics, err := Parse(data, path)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Parse() = descriptor %+v, diagnostics %+v, error %v", descriptor, diagnostics, err)
	}
	return descriptor
}

func writeLoadFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeSkillDocument(t *testing.T, content string) SkillDocument {
	t.Helper()
	const open, closeTag = "<skill-context>\n", "\n</skill-context>"
	encoded, ok := strings.CutPrefix(content, open)
	if !ok {
		t.Fatal("missing typed context envelope")
	}
	encoded, ok = strings.CutSuffix(encoded, closeTag)
	if !ok {
		t.Fatal("missing typed context envelope terminator")
	}
	var doc SkillDocument
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

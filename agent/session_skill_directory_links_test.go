package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

// The named break is discovery skipping immediate-child directory symlinks,
// although identical bytes behind a SKILL.md file symlink already work.
func TestSkillDirectoryLinksPersonalStartup(t *testing.T) {
	// Not parallel: each fixture uses t.Setenv to isolate HOME and XDG roots.
	for _, layout := range []string{"home-agents", "evener-xdg", "evener-home-config"} {
		for _, relative := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/relative=%v", layout, relative), func(t *testing.T) {
				root, project, targets := skillDirectoryLinksFixture(t, layout)
				const name = "linked-personal"
				const body = "DIRECTORY_LINK_STARTUP_3689\n"
				data := []byte("---\nname: linked-personal\ndescription: linked fixture\n---\n" + body)
				target := filepath.Join(targets, "external-source")
				skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), data)
				skillDirectoryLinksWrite(t, filepath.Join(target, "reference.txt"), []byte("COLLATERAL_3689"))

				// Supported-layout control uses the exact same source bytes. Move the
				// control out of discovery before testing the directory link, so it
				// cannot mask a missing linked entry through a same-name collision.
				control := filepath.Join(root, "supported-file-link")
				if err := os.MkdirAll(control, 0o755); err != nil {
					t.Fatal(err)
				}
				skillDirectoryLinksSymlink(t, filepath.Join(target, "SKILL.md"), filepath.Join(control, "SKILL.md"))
				controlSession := skillDirectoryLinksSession(t, project)
				skillDirectoryLinksRequireSource(t, controlSession, name, control)
				skillDirectoryLinksRequirePrepared(t, controlSession, name, "user_slash", control, data, body)
				controlSession.Close()
				if err := os.Rename(control, filepath.Join(targets, "saved-file-link-control")); err != nil {
					t.Fatal(err)
				}

				link := filepath.Join(root, "managed-directory-link")
				linkTarget := target
				if relative {
					var err error
					linkTarget, err = filepath.Rel(root, target)
					if err != nil {
						t.Fatal(err)
					}
				}
				skillDirectoryLinksSymlink(t, linkTarget, link)
				sess := skillDirectoryLinksSession(t, project)
				skillDirectoryLinksRequireSource(t, sess, name, link)
				for _, fallback := range []bool{false, true} {
					if fallback {
						rebuildPromptToolCacheForTest(sess, "read_file")
					}
					prompt, _ := sess.buildPromptData(sess.currentEnv())
					if prompt.HasUseSkill == fallback {
						t.Fatalf("fallback=%v HasUseSkill=%v", fallback, prompt.HasUseSkill)
					}
					if fallback && !prompt.HasTool("read_file") {
						t.Fatal("read_file fallback lost its callable reader")
					}
					entry, ok := skillDirectoryLinksPromptEntry(prompt.Skills, name)
					want := skillEntry{Name: name, CatalogName: name, Description: "linked fixture", Dir: link, SkillFile: filepath.Join(link, "SKILL.md")}
					if !ok || entry != want {
						t.Fatalf("fallback=%v typed skill=%+v present=%v want=%+v", fallback, entry, ok, want)
					}
				}
				status := sess.DetailedStatus()
				skillDirectoryLinksRequirePathFree(t, status)
				if !skillDirectoryLinksCompletionHas(status, name) {
					t.Fatalf("completion lacks %q: %+v", name, status.Skills)
				}
				if _, ok := skillDirectoryLinksInspectionEntry(status, name); !ok {
					t.Fatalf("inspection lacks %q: %+v", name, status.SkillCatalog)
				}
				skillDirectoryLinksRequirePrepared(t, sess, name, "user_slash", link, data, body)
				collateral, err := os.ReadFile(filepath.Join(link, "reference.txt"))
				if err != nil || string(collateral) != "COLLATERAL_3689" {
					t.Fatalf("collateral through lexical base=%q err=%v", collateral, err)
				}
				skillDirectoryLinksRequireBytes(t, filepath.Join(target, "SKILL.md"), data)
				skillDirectoryLinksRequireLink(t, link, linkTarget)
			})
		}
	}
}

// The breaks are bypassing invocation controls for linked sources, hiding
// unavailable/hidden winners from inspection, or leaking paths to completion.
func TestSkillDirectoryLinksInvocationViews(t *testing.T) {
	// Not parallel: skillDirectoryLinksFixture changes process environment.
	root, project, targets := skillDirectoryLinksFixture(t, "evener-xdg")
	fixtures := []struct {
		name, controls string
		model, user    bool
		unavailable    bool
	}{
		{name: "linked-both", model: true, user: true},
		{name: "linked-model", controls: "user-invocable: false\n", model: true},
		{name: "linked-user", controls: "disable-model-invocation: true\n", user: true},
		{name: "linked-hidden", controls: "disable-model-invocation: true\nuser-invocable: false\n"},
		{name: "linked-invalid", controls: "user-invocable: \"true\"\n", unavailable: true},
	}
	for _, fixture := range fixtures {
		data := []byte("---\nname: " + fixture.name + "\ndescription: linked fixture\n" + fixture.controls + "---\nCONTROLS_3689\n")
		target := filepath.Join(targets, fixture.name)
		skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), data)
		skillDirectoryLinksSymlink(t, target, filepath.Join(root, "entry-"+fixture.name))
	}
	sess := skillDirectoryLinksSession(t, project)
	before := sess.Meta().Skills
	for _, fallback := range []bool{false, true} {
		if fallback {
			rebuildPromptToolCacheForTest(sess, "read_file")
		}
		prompt, _ := sess.buildPromptData(sess.currentEnv())
		if prompt.HasUseSkill == fallback {
			t.Fatalf("fallback=%v HasUseSkill=%v", fallback, prompt.HasUseSkill)
		}
		for _, fixture := range fixtures {
			entry, present := skillDirectoryLinksPromptEntry(prompt.Skills, fixture.name)
			if present != fixture.model {
				t.Errorf("fallback=%v model entry %q present=%v want=%v", fallback, fixture.name, present, fixture.model)
			}
			if present && (entry.SkillFile != filepath.Join(root, "entry-"+fixture.name, "SKILL.md") || entry.Dir != filepath.Join(root, "entry-"+fixture.name)) {
				t.Errorf("fallback=%v lexical prompt source=%+v", fallback, entry)
			}
		}
	}
	status := sess.DetailedStatus()
	skillDirectoryLinksRequirePathFree(t, status)
	for _, fixture := range fixtures {
		if present := skillDirectoryLinksCompletionHas(status, fixture.name); present != fixture.user {
			t.Errorf("completion %q present=%v want=%v", fixture.name, present, fixture.user)
		}
		descriptor, present := skillDirectoryLinksInspectionEntry(status, fixture.name)
		if !present || descriptor.Meta.Name != fixture.name || descriptor.Unavailable != fixture.unavailable {
			t.Errorf("inspection %q=%+v present=%v", fixture.name, descriptor, present)
		}
		if !fixture.unavailable && (descriptor.Controls.DisableModelInvocation != !fixture.model || descriptor.Controls.UserInvocable != fixture.user) {
			t.Errorf("inspection controls for %q=%+v", fixture.name, descriptor.Controls)
		}
		for _, route := range []struct {
			name    string
			allowed bool
		}{{"model_tool", fixture.model}, {"user_slash", fixture.user}} {
			batch, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{Name: fixture.name, Route: route.name}})
			if route.allowed {
				if err != nil || batch == nil || len(batch.Items) != 1 || batch.Items[0].Loaded.Body != "CONTROLS_3689\n" {
					t.Errorf("%q route=%s batch=%+v err=%v", fixture.name, route.name, batch, err)
				}
			} else {
				code := "policy_denied"
				if fixture.unavailable {
					code = "invalid_metadata"
				}
				requireActivationError(t, err, code)
				if batch != nil {
					t.Errorf("denied %q route=%s prepared a batch", fixture.name, route.name)
				}
			}
		}
	}
	skillDirectoryLinksRequireDiagnostic(t, status, "invalid_control", "linked-invalid", filepath.Join(root, "entry-linked-invalid", "SKILL.md"))
	if !reflect.DeepEqual(sess.Meta().Skills, before) {
		t.Fatal("preparation changed activation inventory or obligations")
	}
}

// The breaks are silently ignoring broken/looping links, disabling healthy
// siblings, wedging a restored target, or accepting changed declared identity.
func TestSkillDirectoryLinksTargetRecoveryPreservesSiblings(t *testing.T) {
	// Not parallel: skillDirectoryLinksFixture changes process environment.
	root, project, targets := skillDirectoryLinksFixture(t, "home-agents")
	const name = "linked-recovery"
	const body = "RECOVERY_ORIGINAL_3689\n"
	data := []byte("---\nname: linked-recovery\ndescription: linked fixture\n---\n" + body)
	target := filepath.Join(targets, "recoverable")
	link := filepath.Join(root, "recoverable-link")
	skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), data)
	skillDirectoryLinksSymlink(t, target, link)
	pendingTarget := filepath.Join(targets, "pending")
	pendingLink := filepath.Join(root, "pending-link")
	skillDirectoryLinksSymlink(t, pendingTarget, pendingLink)
	loop := filepath.Join(root, "loop-link")
	skillDirectoryLinksSymlink(t, "loop-link", loop)

	// Healthy real-directory, file-linked, and directory-linked siblings must
	// stay usable through every failed load and rediscovery.
	siblings := []struct{ name, dir, source, linkTarget string }{
		{name: "real-sibling", dir: filepath.Join(root, "real-sibling")},
		{name: "file-sibling", dir: filepath.Join(root, "file-sibling"), source: filepath.Join(targets, "file-source", "SKILL.md")},
		{name: "directory-sibling", dir: filepath.Join(root, "directory-sibling"), linkTarget: filepath.Join(targets, "directory-source")},
	}
	for _, sibling := range siblings {
		source := filepath.Join(sibling.dir, "SKILL.md")
		if sibling.source != "" {
			source = sibling.source
		} else if sibling.linkTarget != "" {
			source = filepath.Join(sibling.linkTarget, "SKILL.md")
		}
		skillDirectoryLinksWrite(t, source, []byte("---\nname: "+sibling.name+"\ndescription: linked fixture\n---\nSIBLING_3689\n"))
		if sibling.source != "" {
			if err := os.MkdirAll(sibling.dir, 0o755); err != nil {
				t.Fatal(err)
			}
			skillDirectoryLinksSymlink(t, source, filepath.Join(sibling.dir, "SKILL.md"))
		} else if sibling.linkTarget != "" {
			skillDirectoryLinksSymlink(t, sibling.linkTarget, sibling.dir)
		}
	}
	requireSiblings := func(sess *Session) {
		t.Helper()
		for _, sibling := range siblings {
			bytes := []byte("---\nname: " + sibling.name + "\ndescription: linked fixture\n---\nSIBLING_3689\n")
			skillDirectoryLinksRequireSource(t, sess, sibling.name, sibling.dir)
			skillDirectoryLinksRequirePrepared(t, sess, sibling.name, "user_slash", sibling.dir, bytes, "SIBLING_3689\n")
			skillDirectoryLinksRequireBytes(t, filepath.Join(sibling.dir, "SKILL.md"), bytes)
			if sibling.source != "" {
				skillDirectoryLinksRequireLink(t, filepath.Join(sibling.dir, "SKILL.md"), sibling.source)
			} else if sibling.linkTarget != "" {
				skillDirectoryLinksRequireLink(t, sibling.dir, sibling.linkTarget)
			}
		}
	}
	sess := skillDirectoryLinksSession(t, project)
	before := sess.Meta().Skills
	skillDirectoryLinksRequireSource(t, sess, name, link)
	skillDirectoryLinksRequirePrepared(t, sess, name, "user_slash", link, data, body)
	requireSiblings(sess)
	initial := sess.DetailedStatus()
	skillDirectoryLinksRequireDiagnostic(t, initial, "unreadable_source", "", pendingLink)
	skillDirectoryLinksRequireDiagnostic(t, initial, "unreadable_source", "", loop)
	for _, phantom := range []string{"pending-link", "loop-link"} {
		if _, err := sess.skills.ResolveExact(phantom); err == nil {
			t.Fatalf("unreadable link invented catalog identity %q", phantom)
		}
	}

	parked := filepath.Join(targets, "recoverable-offline")
	if err := os.Rename(target, parked); err != nil {
		t.Fatal(err)
	}
	batch, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{Name: name, Route: "user_slash"}})
	requireActivationError(t, err, "source_missing")
	if batch != nil {
		t.Fatal("missing target prepared instructions")
	}
	requireSiblings(sess)
	missing := skillDirectoryLinksSession(t, project)
	skillDirectoryLinksRequireDiagnostic(t, missing.DetailedStatus(), "unreadable_source", "", link)
	if _, err := missing.skills.ResolveExact(name); err == nil {
		t.Fatal("fresh session retained an unreadable linked skill")
	}
	requireSiblings(missing)
	if err := os.Rename(parked, target); err != nil {
		t.Fatal(err)
	}
	// Existing descriptors load again without a catalog refresh.
	skillDirectoryLinksRequirePrepared(t, sess, name, "user_slash", link, data, body)
	pendingData := []byte("---\nname: linked-restored\ndescription: linked fixture\n---\nRESTORED_3689\n")
	skillDirectoryLinksWrite(t, filepath.Join(pendingTarget, "SKILL.md"), pendingData)
	restored := skillDirectoryLinksSession(t, project)
	skillDirectoryLinksRequireSource(t, restored, name, link)
	skillDirectoryLinksRequireSource(t, restored, "linked-restored", pendingLink)
	skillDirectoryLinksRequirePrepared(t, restored, "linked-restored", "user_slash", pendingLink, pendingData, "RESTORED_3689\n")
	for _, diagnostic := range restored.DetailedStatus().SkillDiagnostics {
		if diagnostic.Category == "unreadable_source" && (diagnostic.Source == link || diagnostic.Source == pendingLink) {
			t.Fatalf("restored target kept a stale diagnostic: %+v", diagnostic)
		}
	}
	skillDirectoryLinksRequireDiagnostic(t, restored.DetailedStatus(), "unreadable_source", "", loop)
	requireSiblings(restored)

	changedIdentity := []byte("---\nname: renamed-recovery\ndescription: linked fixture\n---\nCHANGED_IDENTITY_3689\n")
	skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), changedIdentity)
	batch, err = sess.prepareSkillActivations(context.Background(), []skillInvocation{{Name: name, Route: "user_slash"}})
	requireActivationError(t, err, "source_changed")
	if batch != nil {
		t.Fatal("identity-changed target prepared instructions")
	}
	requireSiblings(sess)
	// Same-name replacements retain the current-byte policy, not inode or
	// discovery-time digest pinning.
	currentData := []byte("---\nname: linked-recovery\ndescription: linked fixture\n---\nCURRENT_BYTES_3689\n")
	skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), currentData)
	skillDirectoryLinksRequirePrepared(t, sess, name, "user_slash", link, currentData, "CURRENT_BYTES_3689\n")
	skillDirectoryLinksWrite(t, filepath.Join(target, "SKILL.md"), data)
	skillDirectoryLinksRequirePrepared(t, sess, name, "user_slash", link, data, body)
	skillDirectoryLinksRequireBytes(t, filepath.Join(target, "SKILL.md"), data)
	skillDirectoryLinksRequireBytes(t, filepath.Join(pendingTarget, "SKILL.md"), pendingData)
	skillDirectoryLinksRequireLink(t, link, target)
	skillDirectoryLinksRequireLink(t, pendingLink, pendingTarget)
	skillDirectoryLinksRequireLink(t, loop, "loop-link")
	if !reflect.DeepEqual(sess.Meta().Skills, before) {
		t.Fatal("failed or recovered preparation changed activation inventory or obligations")
	}
}

func skillDirectoryLinksFixture(t *testing.T, layout string) (root, project, targets string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(base, "config"),
		"XDG_STATE_HOME": filepath.Join(base, "state"), "XDG_CACHE_HOME": filepath.Join(base, "cache"),
		"XDG_DATA_HOME": filepath.Join(base, "data"),
	} {
		t.Setenv(key, value)
	}
	// Save/restore the ambient value, but actually unset the disable switch.
	t.Setenv(envvars.EVENERNoUserSkills.Name, "")
	if err := os.Unsetenv(envvars.EVENERNoUserSkills.Name); err != nil {
		t.Fatal(err)
	}
	switch layout {
	case "home-agents":
		root = filepath.Join(home, ".agents", "skills")
	case "evener-xdg":
		root = filepath.Join(base, "config", "evener", "skills")
	case "evener-home-config":
		t.Setenv("XDG_CONFIG_HOME", "")
		if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(home, ".config", "evener", "skills")
	default:
		t.Fatalf("unknown fixture layout %q", layout)
	}
	project, targets = filepath.Join(base, "project"), filepath.Join(base, "external-targets")
	for _, dir := range []string{home, root, project, targets} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	markGitRoot(t, project)
	return root, project, targets
}

func skillDirectoryLinksSession(t *testing.T, project string) *Session {
	t.Helper()
	// No adapter or ProcessInput: startup, prompt/status reads and preparation
	// exercise production plumbing without issuing any provider request.
	sess, err := NewSession(llm.NewClient(), newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(project), SessionConfig{
		AgentsDocPath: filepath.Join(project, "absent-personal-AGENTS.md"),
		testOnly:      testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func skillDirectoryLinksWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillDirectoryLinksSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink %q -> %q: %v", link, target, err)
	}
}

func skillDirectoryLinksRequireSource(t *testing.T, sess *Session, name, dir string) {
	t.Helper()
	descriptor, err := sess.skills.ResolveExact(name)
	if err != nil {
		t.Fatalf("session startup skipped %q at %s: %v", name, dir, err)
	}
	if descriptor.CatalogName != name || descriptor.Meta.Name != name || descriptor.Meta.Dir != dir || descriptor.Meta.SkillFile != filepath.Join(dir, "SKILL.md") || descriptor.Unavailable {
		t.Fatalf("discovered lexical source=%+v want name=%q dir=%q", descriptor, name, dir)
	}
}

func skillDirectoryLinksRequirePrepared(t *testing.T, sess *Session, name, route, dir string, source []byte, body string) {
	t.Helper()
	batch, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{Name: name, Route: route}})
	if err != nil || batch == nil || len(batch.Items) != 1 {
		t.Fatalf("prepare %q route=%s batch=%+v err=%v", name, route, batch, err)
	}
	item := batch.Items[0]
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(source))
	if item.Loaded.Body != body || item.Loaded.Digest != wantDigest || item.Loaded.Descriptor.CatalogName != name || item.Loaded.Descriptor.Meta.SkillFile != filepath.Join(dir, "SKILL.md") || item.Loaded.Descriptor.Meta.Dir != dir {
		t.Fatalf("prepared linked source=%+v want body=%q digest=%s dir=%q", item.Loaded, body, wantDigest, dir)
	}
	// Decode the machine-readable skill envelope, not assembled prompt prose.
	encoded := strings.TrimSuffix(strings.TrimPrefix(item.Rendered.Content, "<skill-context>\n"), "\n</skill-context>")
	var document struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Source        string `json:"source"`
		BaseDirectory string `json:"base_directory"`
		Instructions  string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("decode skill envelope: %v", err)
	}
	if document.Name != name || document.Description != "linked fixture" || document.Source != filepath.Join(dir, "SKILL.md") || document.BaseDirectory != dir || document.Instructions != body {
		t.Fatalf("rendered skill document=%+v want lexical source/base %q", document, dir)
	}
}

func skillDirectoryLinksPromptEntry(entries []skillEntry, name string) (skillEntry, bool) {
	for _, entry := range entries {
		if entry.CatalogName == name {
			return entry, true
		}
	}
	return skillEntry{}, false
}

func skillDirectoryLinksCompletionHas(status DetailedStatus, name string) bool {
	for _, entry := range status.Skills {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func skillDirectoryLinksInspectionEntry(status DetailedStatus, name string) (skill.Descriptor, bool) {
	for _, entry := range status.SkillCatalog {
		if entry.CatalogName == name {
			return entry, true
		}
	}
	return skill.Descriptor{}, false
}

func skillDirectoryLinksRequirePathFree(t *testing.T, status DetailedStatus) {
	t.Helper()
	for _, entry := range status.Skills {
		if entry.Dir != "" || entry.SkillFile != "" {
			t.Fatalf("completion leaked source paths: %+v", entry)
		}
	}
	for _, entry := range status.SkillCatalog {
		if entry.Meta.Dir != "" || entry.Meta.SkillFile != "" {
			t.Fatalf("inspection leaked source paths: %+v", entry)
		}
	}
}

func skillDirectoryLinksRequireDiagnostic(t *testing.T, status DetailedStatus, category, name, source string) {
	t.Helper()
	for _, diagnostic := range status.SkillDiagnostics {
		if diagnostic.Category == category && diagnostic.Name == name && diagnostic.Source == source {
			return
		}
	}
	t.Fatalf("missing diagnostic category=%q name=%q source=%q in %+v", category, name, source, status.SkillDiagnostics)
}

func skillDirectoryLinksRequireBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("fixture bytes changed at %q: got=%q want=%q err=%v", path, got, want, err)
	}
}

func skillDirectoryLinksRequireLink(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil || got != want {
		t.Fatalf("fixture link changed at %q: got=%q want=%q err=%v", path, got, want, err)
	}
}

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// SystemPromptAppend files are documented as always applied. A file that cannot
// be read is still skipped, but the omission must be disclosed through the
// existing render-warning channel instead of silently dropping the operator's
// instructions (CORE-08).
func TestStartupPromptDisclosesUnreadableAppend(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "extra.md")
	const goodSentinel = "GOOD_APPEND_SENTINEL"
	if err := os.WriteFile(good, []byte(goodSentinel), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.md")

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{good, missing},
	}))
	if !slices.ContainsFunc(drainBufferedWarnings(sess), func(warning string) bool {
		return strings.Contains(warning, missing)
	}) {
		t.Fatal("an unreadable SystemPromptAppend file was skipped without any diagnostic")
	}
	if !strings.Contains(sess.cachedSystemPrompt, goodSentinel) {
		t.Fatalf("the readable append file was not applied:\n%s", sess.cachedSystemPrompt)
	}
}

func TestStartupPromptRetainsAppendedFileAfterRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instructions")
	const contents = "append-27\t \r\nappend-61\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := newSession(t, withConfig(SessionConfig{SystemPromptAppend: []string{path}}))
	startup := sess.cachedSystemPrompt
	if !strings.Contains(startup, "append-27\t \r\nappend-61") {
		t.Fatal("startup omitted the append file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if sess.cachedSystemPrompt != startup {
		t.Fatal("model switch changed the saved prompt after its append file disappeared")
	}
}

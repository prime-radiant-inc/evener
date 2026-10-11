package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSessionMetaIgnoresTheRetiredNoProjectPromptsKey pins that a meta
// written by a build that still had the no_project_prompts setting keeps
// loading after the setting's removal.
func TestLoadSessionMetaIgnoresTheRetiredNoProjectPromptsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "retiredkey1"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Config: ConfigSnapshot{NonInteractive: true}}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	path := filepath.Join(dir, sessionsSubdir, id+".meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	config, ok := doc["config"].(map[string]any)
	if !ok {
		t.Fatalf("meta has no config object: %s", raw)
	}
	config["no_project_prompts"] = true
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if !got.Config.NonInteractive {
		t.Fatalf("config = %+v, want the rest of the meta intact", got.Config)
	}
}

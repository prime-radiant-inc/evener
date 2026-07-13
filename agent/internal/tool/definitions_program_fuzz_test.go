package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// FuzzToolDefinitionsProgram builds every shipped tool definition, registers it
// in the real Registry, and verifies that construction is deterministic and
// does not share schema maps with registration-time purpose normalization. The
// fuzz input varies the enum-bearing definitions (delegate, job_watch, and
// task_list) and the custom communicate name; every generated value is encoded
// into a valid tool identifier, so invalid-name rejection is not mistaken for a
// definition bug.
func FuzzToolDefinitionsProgram(f *testing.F) {
	for _, seed := range []struct {
		label   string
		variant uint8
	}{
		{label: "standard", variant: 0},
		{label: "one", variant: 1},
		{label: "multi value", variant: 2},
		{label: "unicode \U0001f642", variant: 255},
	} {
		f.Add(seed.label, seed.variant)
	}

	f.Fuzz(func(t *testing.T, label string, variant uint8) {
		if len(label) > 256 {
			return
		}
		agentTypes := toolProgramVocabulary("agent", label, variant)
		eventKinds := toolProgramVocabulary("event", label, variant+1)
		effortLevels := toolProgramVocabulary("effort", label, variant+2)
		customName := "communicate_" + toolProgramPayload(label)
		if len(customName) > 64 {
			customName = customName[:64]
		}

		// Empty and non-empty values each have meaningful definition branches.
		// Check both independently, then use the fuzz-varying versions below for
		// the package-wide registration pass.
		toolProgramDefinitionShape(t, DefDelegate(nil))
		toolProgramDefinitionShape(t, DefDelegate([]string{"analysis"}))
		toolProgramDefinitionShape(t, DefJobWatch(nil))
		toolProgramDefinitionShape(t, DefJobWatch([]string{"output"}))
		toolProgramDefinitionShape(t, DefTaskList(nil))
		toolProgramDefinitionShape(t, DefTaskList([]string{"low"}))

		defs := toolProgramDefinitions(agentTypes, eventKinds, effortLevels, customName)
		again := toolProgramDefinitions(agentTypes, eventKinds, effortLevels, customName)
		if len(defs) != len(again) || len(defs) == 0 {
			t.Fatalf("definition count = %d / %d", len(defs), len(again))
		}

		reg := NewRegistry()
		seen := map[string]bool{}
		for i, def := range defs {
			toolProgramDefinitionShape(t, def)
			before, err := json.Marshal(def.Parameters)
			if err != nil {
				t.Fatalf("marshal %s schema before registration: %v", def.Name, err)
			}
			if seen[def.Name] {
				t.Fatalf("duplicate definition %q", def.Name)
			}
			seen[def.Name] = true
			if defs[i].Name != again[i].Name {
				t.Fatalf("definition order changed at %d: %q != %q", i, defs[i].Name, again[i].Name)
			}
			second, err := json.Marshal(again[i].Parameters)
			if err != nil || !bytes.Equal(before, second) {
				t.Fatalf("definition %q is not deterministic: first=%s second=%s err=%v", def.Name, before, second, err)
			}

			if err := reg.Register(RegisteredTool{
				Tool:        llm.Tool{Definition: def},
				OmitPurpose: i%2 == 0,
				Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, _ map[string]any) (any, error) {
					return "definition program", nil
				},
			}); err != nil {
				t.Fatalf("register %s: %v", def.Name, err)
			}
			after, err := json.Marshal(def.Parameters)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("registration mutated %s schema: before=%s after=%s err=%v", def.Name, before, after, err)
			}
			registered := reg.Get(def.Name)
			if registered == nil || registered.Schema == nil || registered.Exec == nil {
				t.Fatalf("registered definition %q is incomplete: %+v", def.Name, registered)
			}
		}

		if got := reg.Names(); len(got) != len(seen) {
			t.Fatalf("registry names = %d, want %d: %v", len(got), len(seen), got)
		}
		if got := reg.Definitions(); len(got) != len(seen) {
			t.Fatalf("registry definitions = %d, want %d", len(got), len(seen))
		}
		clone := reg.Clone()
		clone.Remove(defs[0].Name)
		if reg.Get(defs[0].Name) == nil || clone.Get(defs[0].Name) != nil {
			t.Fatalf("definition registry clone shares its map for %q", defs[0].Name)
		}

		toolProgramSchemaClone(t)
	})
}

func toolProgramDefinitions(agentTypes, eventKinds, effortLevels []string, customName string) []llm.ToolDefinition {
	return []llm.ToolDefinition{
		DefReadFile(),
		DefWriteFile(),
		DefListDir(),
		DefEditFile(),
		DefShell(),
		DefDelegate(agentTypes),
		DefDelegateSend(),
		DefJobWatch(eventKinds),
		DefJobStatus(),
		DefJobReadOutput(),
		DefJobList(),
		DefJobStop(),
		DefGrep(),
		DefGlob(),
		DefApplyPatch(),
		DefWebFetch(),
		DefWebSearch(),
		DefCommunicate(),
		DefCommunicateNamed(customName),
		DefTaskList(effortLevels),
		DefUseSkill(),
		DefFindSessionTranscripts(),
		DefReadSessionTranscript(),
		DefManageWorktree(),
		DefReadTranscript(),
		DefAskUser(),
	}
}

func toolProgramDefinitionShape(t *testing.T, def llm.ToolDefinition) {
	t.Helper()
	if def.Name == "" || def.Description == "" || def.Parameters == nil {
		t.Fatalf("incomplete definition: %+v", def)
	}
	if typ, _ := def.Parameters["type"].(string); typ != "object" {
		t.Fatalf("definition %q root type = %q, want object", def.Name, typ)
	}
	withPurpose := WithPurposeParameter(def)
	props, _ := withPurpose.Parameters["properties"].(map[string]any)
	if props == nil || props["purpose"] == nil {
		t.Fatalf("definition %q did not gain purpose: %#v", def.Name, withPurpose.Parameters)
	}
	withoutPurpose := WithoutPurposeParameter(withPurpose)
	withoutProps, _ := withoutPurpose.Parameters["properties"].(map[string]any)
	if withoutProps != nil && withoutProps["purpose"] != nil {
		t.Fatalf("definition %q retained purpose after removal: %#v", def.Name, withoutPurpose.Parameters)
	}
}

func toolProgramVocabulary(prefix, _ string, variant uint8) []string {
	// Registry caches compiled schemas globally by their JSON representation.
	// Keep enum values finite during mutation fuzzing so this target does not
	// turn an arbitrary byte stream into an unbounded process-lifetime cache.
	value := prefix + "_one"
	switch variant % 3 {
	case 0:
		return nil
	case 1:
		return []string{value}
	default:
		return []string{value, prefix + "_two"}
	}
}

func toolProgramSchemaClone(t *testing.T) {
	t.Helper()
	original := map[string]any{
		"properties": map[string]any{"nested": map[string]any{"enum": []string{"a", "b"}}},
		"required":   []any{"nested"},
	}
	clone := CloneSchemaMap(original)
	clone["properties"].(map[string]any)["nested"].(map[string]any)["enum"].([]string)[0] = "changed"
	clone["required"].([]any)[0] = "changed"
	if got := original["properties"].(map[string]any)["nested"].(map[string]any)["enum"].([]string)[0]; got != "a" {
		t.Fatalf("CloneSchemaMap shared nested enum: %q", got)
	}
	if got := original["required"].([]any)[0]; got != "nested" {
		t.Fatalf("CloneSchemaMap shared required slice: %q", got)
	}
}

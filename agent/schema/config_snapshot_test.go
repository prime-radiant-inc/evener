package schema

import (
	"reflect"
	"testing"
)

func TestConfigSnapshotCloneDeepCopiesReferenceFields(t *testing.T) {
	loopDetection := false
	sandboxNet := false
	original := ConfigSnapshot{
		ToolOutputLimits: map[string]ToolOutputLimit{
			"read_file": {MaxChars: 100, MaxLines: 10, Strategy: TruncHeadTail},
		},
		SkillsDirs:          []string{"skills"},
		MCPConfigFiles:      []string{"mcp.json"},
		MCPInline:           []string{"inline"},
		PluginDirs:          []string{"plugins"},
		SystemPromptAppend:  []string{"append.md"},
		EnableLoopDetection: &loopDetection,
		ModelFallbacks:      []string{"openai/fallback"},
		SandboxNet:          &sandboxNet,
	}
	want := original
	want.ToolOutputLimits = map[string]ToolOutputLimit{
		"read_file": original.ToolOutputLimits["read_file"],
	}
	want.SkillsDirs = append([]string(nil), original.SkillsDirs...)
	want.MCPConfigFiles = append([]string(nil), original.MCPConfigFiles...)
	want.MCPInline = append([]string(nil), original.MCPInline...)
	want.PluginDirs = append([]string(nil), original.PluginDirs...)
	want.SystemPromptAppend = append([]string(nil), original.SystemPromptAppend...)
	wantLoopDetection := *original.EnableLoopDetection
	want.EnableLoopDetection = &wantLoopDetection
	want.ModelFallbacks = append([]string(nil), original.ModelFallbacks...)
	wantSandboxNet := *original.SandboxNet
	want.SandboxNet = &wantSandboxNet

	clone := original.Clone()
	clone.ToolOutputLimits["read_file"] = ToolOutputLimit{MaxChars: 999, Strategy: TruncTail}
	clone.SkillsDirs[0] = "mutated-skills"
	clone.MCPConfigFiles[0] = "mutated-mcp.json"
	clone.MCPInline[0] = "mutated-inline"
	clone.PluginDirs[0] = "mutated-plugins"
	clone.SystemPromptAppend[0] = "mutated-append.md"
	*clone.EnableLoopDetection = true
	clone.ModelFallbacks[0] = "openai/mutated"
	*clone.SandboxNet = true

	if !reflect.DeepEqual(original, want) {
		t.Fatalf("Clone aliases a reference-valued field:\n got %#v\nwant %#v", original, want)
	}
}

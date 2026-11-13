package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

func findToolDef(defs []llm.ToolDefinition, name string) (llm.ToolDefinition, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d, true
		}
	}
	return llm.ToolDefinition{}, false
}

func toolProp(td llm.ToolDefinition, prop string) (map[string]any, bool) {
	if td.Parameters == nil {
		return nil, false
	}
	props, ok := td.Parameters["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	p, ok := props[prop].(map[string]any)
	return p, ok
}

// Regime 1: Anthropic Claude sees both image/png and application/pdf natively inline.
// vision_prompt is omitted from advertised properties and description.
func TestSessionReadFileSchema_Regime1_AnthropicClaude(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("anthropic", nil))
	prof, err := provider.Resolve(provider.EmbeddedRegistry(), "anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("resolve claude: %v", err)
	}
	sess := newSession(t, withClient(client), withProfile(prof),
		withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))

	defs := sess.ToolDefinitions()
	rf, ok := findToolDef(defs, "read_file")
	if !ok {
		t.Fatal("read_file not advertised")
	}
	if _, hasVisionPrompt := toolProp(rf, "vision_prompt"); hasVisionPrompt {
		t.Fatalf("read_file advertised vision_prompt in Regime 1; def: %+v", rf)
	}
	wantRFDesc := "Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis."
	if rf.Description != wantRFDesc {
		t.Fatalf("read_file description in Regime 1:\ngot:  %q\nwant: %q", rf.Description, wantRFDesc)
	}

	mr, ok := findToolDef(defs, "memory_read")
	if !ok {
		t.Fatal("memory_read not advertised")
	}
	if _, hasVisionPrompt := toolProp(mr, "vision_prompt"); hasVisionPrompt {
		t.Fatalf("memory_read advertised vision_prompt in Regime 1; def: %+v", mr)
	}
	wantMRDesc := "Operate on a relative path in the bound personal or project memory wiki. Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis. Reading MEMORY.md at the scope root returns the whole generated index."
	if mr.Description != wantMRDesc {
		t.Fatalf("memory_read description in Regime 1:\ngot:  %q\nwant: %q", mr.Description, wantMRDesc)
	}
}

// Regime 1: Explorer agents do not run vision side-channel calls.
func TestSessionReadFileSchema_Regime1_Explorer(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("openai", nil))
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{AgentName: "explorer"}))

	defs := sess.ToolDefinitions()
	rf, ok := findToolDef(defs, "read_file")
	if !ok {
		t.Fatal("read_file not advertised")
	}
	if _, hasVisionPrompt := toolProp(rf, "vision_prompt"); hasVisionPrompt {
		t.Fatalf("explorer read_file advertised vision_prompt; def: %+v", rf)
	}
	wantDesc := "Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis."
	if rf.Description != wantDesc {
		t.Fatalf("explorer read_file description:\ngot:  %q\nwant: %q", rf.Description, wantDesc)
	}
}

// Regime 1: VisionModel set to off, none, or disabled disables the side-channel.
func TestSessionReadFileSchema_Regime1_VisionOff(t *testing.T) {
	t.Parallel()
	for _, setting := range []string{"off", "none", "disabled"} {
		t.Run(setting, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(newFakeErrAdapter("openai", nil))
			sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
				withConfig(SessionConfig{VisionModel: setting}))

			defs := sess.ToolDefinitions()
			rf, ok := findToolDef(defs, "read_file")
			if !ok {
				t.Fatal("read_file not advertised")
			}
			if _, hasVisionPrompt := toolProp(rf, "vision_prompt"); hasVisionPrompt {
				t.Fatalf("read_file advertised vision_prompt when VisionModel=%q", setting)
			}
		})
	}
}

// Regime 2: OpenAI gpt-5.2 sees images natively inline, but PDFs use the side-channel.
// vision_prompt is kept and scoped to PDFs.
func TestSessionReadFileSchema_Regime2_OpenAIGpt52(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("openai", nil))
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))

	defs := sess.ToolDefinitions()
	rf, ok := findToolDef(defs, "read_file")
	if !ok {
		t.Fatal("read_file not advertised")
	}
	vp, hasVisionPrompt := toolProp(rf, "vision_prompt")
	if !hasVisionPrompt {
		t.Fatalf("read_file omitted vision_prompt in Regime 2; def: %+v", rf)
	}
	wantVPDesc := "PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate."
	if gotVPDesc, _ := vp["description"].(string); gotVPDesc != wantVPDesc {
		t.Fatalf("read_file vision_prompt description in Regime 2:\ngot:  %q\nwant: %q", gotVPDesc, wantVPDesc)
	}
	wantRFDesc := "Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis. When reading a PDF, put what you hope to learn in the `vision_prompt` argument — the system will provide a detailed description alongside the file."
	if rf.Description != wantRFDesc {
		t.Fatalf("read_file description in Regime 2:\ngot:  %q\nwant: %q", rf.Description, wantRFDesc)
	}

	mr, ok := findToolDef(defs, "memory_read")
	if !ok {
		t.Fatal("memory_read not advertised")
	}
	vpMR, hasVPMR := toolProp(mr, "vision_prompt")
	if !hasVPMR {
		t.Fatalf("memory_read omitted vision_prompt in Regime 2; def: %+v", mr)
	}
	if gotVPDesc, _ := vpMR["description"].(string); gotVPDesc != wantVPDesc {
		t.Fatalf("memory_read vision_prompt description in Regime 2:\ngot:  %q\nwant: %q", gotVPDesc, wantVPDesc)
	}
	wantMRDesc := "Operate on a relative path in the bound personal or project memory wiki. Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis. When reading a PDF, put what you hope to learn in the `vision_prompt` argument — the system will provide a detailed description alongside the file. Reading MEMORY.md at the scope root returns the whole generated index."
	if mr.Description != wantMRDesc {
		t.Fatalf("memory_read description in Regime 2:\ngot:  %q\nwant: %q", mr.Description, wantMRDesc)
	}
}

// Regime 3: Text-only model keeps vision_prompt for both image and PDF reads.
func TestSessionReadFileSchema_Regime3_TextOnlyPrimary(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("openai", nil))
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2-codex")),
		withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))

	defs := sess.ToolDefinitions()
	rf, ok := findToolDef(defs, "read_file")
	if !ok {
		t.Fatal("read_file not advertised")
	}
	vp, hasVisionPrompt := toolProp(rf, "vision_prompt")
	if !hasVisionPrompt {
		t.Fatalf("read_file omitted vision_prompt in Regime 3; def: %+v", rf)
	}
	wantVPDesc := "Image/PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate."
	if gotVPDesc, _ := vp["description"].(string); gotVPDesc != wantVPDesc {
		t.Fatalf("read_file vision_prompt description in Regime 3:\ngot:  %q\nwant: %q", gotVPDesc, wantVPDesc)
	}
	wantRFDesc := tool.DefReadFile().Description
	if rf.Description != wantRFDesc {
		t.Fatalf("read_file description in Regime 3:\ngot:  %q\nwant: %q", rf.Description, wantRFDesc)
	}
}

// Regime 3: Explicitly configured VisionModel keeps side-channel for both.
func TestSessionReadFileSchema_Regime3_ExplicitVisionModel(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("openai", nil))
	client.Register(newFakeErrAdapter("anthropic", nil))
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{VisionModel: "anthropic/claude-x"}))

	defs := sess.ToolDefinitions()
	rf, ok := findToolDef(defs, "read_file")
	if !ok {
		t.Fatal("read_file not advertised")
	}
	vp, hasVisionPrompt := toolProp(rf, "vision_prompt")
	if !hasVisionPrompt {
		t.Fatalf("read_file omitted vision_prompt in Regime 3 explicit; def: %+v", rf)
	}
	wantVPDesc := "Image/PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate."
	if gotVPDesc, _ := vp["description"].(string); gotVPDesc != wantVPDesc {
		t.Fatalf("read_file vision_prompt description in Regime 3 explicit:\ngot:  %q\nwant: %q", gotVPDesc, wantVPDesc)
	}
}

// SetModel dynamically switches regimes and updates ToolDefinitions.
func TestSessionReadFileSchema_SetModelDynamicSwitching(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("openai", nil))
	client.Register(newFakeErrAdapter("anthropic", nil))
	sess := newSession(t, withClient(client), withProfile(NewOpenAIProfile("gpt-5.2")),
		withConfig(SessionConfig{
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(provider.EmbeddedRegistry(), ref)
			},
		}))

	// Starts in Regime 2
	defs := sess.ToolDefinitions()
	rf, _ := findToolDef(defs, "read_file")
	vp, hasVP := toolProp(rf, "vision_prompt")
	if !hasVP || vp["description"] != "PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate." {
		t.Fatalf("expected Regime 2 at start; def: %+v", rf)
	}

	// Switch to Claude -> Regime 1 (neither image nor PDF uses side-channel)
	if err := sess.SetModel("anthropic/claude-opus-4-5"); err != nil {
		t.Fatalf("SetModel claude: %v", err)
	}
	defs = sess.ToolDefinitions()
	rf, _ = findToolDef(defs, "read_file")
	if _, hasVP := toolProp(rf, "vision_prompt"); hasVP {
		t.Fatalf("expected Regime 1 after switch to claude; def: %+v", rf)
	}

	// Switch to text-only codex -> Regime 3 (both use side-channel)
	if err := sess.SetModel("openai/gpt-5.2-codex"); err != nil {
		t.Fatalf("SetModel codex: %v", err)
	}
	defs = sess.ToolDefinitions()
	rf, _ = findToolDef(defs, "read_file")
	vp, hasVP = toolProp(rf, "vision_prompt")
	if !hasVP || vp["description"] != "Image/PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate." {
		t.Fatalf("expected Regime 3 after switch to codex; def: %+v", rf)
	}

	// SetVisionModel off -> Regime 1
	if err := sess.SetVisionModel("off"); err != nil {
		t.Fatalf("SetVisionModel off: %v", err)
	}
	defs = sess.ToolDefinitions()
	rf, _ = findToolDef(defs, "read_file")
	if _, hasVP := toolProp(rf, "vision_prompt"); hasVP {
		t.Fatalf("expected Regime 1 after SetVisionModel off; def: %+v", rf)
	}
}

// Critical requirement: s.reg's underlying validation schema accepts vision_prompt
// in all regimes so callers that pass it never error with additionalProperties: false.
func TestSessionReadFileSchema_RegistryValidationAcceptsVisionPromptInAllRegimes(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(newFakeErrAdapter("anthropic", nil))
	prof, err := provider.Resolve(provider.EmbeddedRegistry(), "anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("resolve claude: %v", err)
	}
	memDir := t.TempDir()
	sess := newSession(t, withClient(client), withProfile(prof), withDir(t.TempDir()),
		withConfig(SessionConfig{MemoryStateRoot: memDir}))

	// Verify we are in Regime 1 (vision_prompt omitted from wire)
	rf, _ := findToolDef(sess.ToolDefinitions(), "read_file")
	if _, hasVP := toolProp(rf, "vision_prompt"); hasVP {
		t.Fatal("read_file should omit vision_prompt on the wire in Regime 1")
	}

	// Create test file
	tmpFile := filepath.Join(sess.env.WorkingDirectory(), "test.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Call read_file with vision_prompt via registry
	regTool := sess.reg.Get("read_file")
	if regTool == nil {
		t.Fatal("read_file not registered in s.reg")
	}
	args := map[string]any{
		"file_path":     "test.txt",
		"vision_prompt": "describe it please",
		"intent":        "Reading file for test",
	}
	if err := regTool.Schema.Validate(args); err != nil {
		t.Fatalf("s.reg schema rejected vision_prompt in Regime 1: %v", err)
	}
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "call_rf", Name: "read_file",
		Arguments: []byte(`{"file_path":"test.txt","vision_prompt":"describe it please","intent":"Reading file for test"}`),
	})
	if res.IsError {
		t.Fatalf("ExecuteCall read_file with vision_prompt failed in Regime 1: %v", res.Output)
	}

	// Call memory_read with vision_prompt via registry
	memTool := sess.reg.Get("memory_read")
	if memTool == nil {
		t.Fatal("memory_read not registered in s.reg")
	}
	memArgs := map[string]any{
		"file_path":     "MEMORY.md",
		"scope":         "personal",
		"vision_prompt": "describe it please",
		"intent":        "Reading memory for test",
	}
	if err := memTool.Schema.Validate(memArgs); err != nil {
		t.Fatalf("s.reg schema rejected memory_read vision_prompt in Regime 1: %v", err)
	}
}

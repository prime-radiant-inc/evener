package agent

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestDescribeImage_ExactTranscriptionPurposeRequestsLiteralCharacters(t *testing.T) {
	t.Parallel()

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("flagcodes_iz_challenge}")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("vision-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	registry := tool.NewRegistry()
	if err := registerFileTools(registry, &toolDeps{readGuard: readGuard{trackRead: func(string) {}}}); err != nil {
		t.Fatalf("registerFileTools: %v", err)
	}
	call := llm.ToolCallData{
		ID:        "call-image",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"picture.png","purpose":"Transcribe the rendered text exactly, including every case and symbol."}`),
	}
	result := registry.ExecuteCall(context.Background(), &w3sub_readFileEnv{
		output: "[image: picture.png]\n" + base64.StdEncoding.EncodeToString(validPNGFixture(t)),
	}, call)
	if result.IsError || result.ImagePurpose != "Transcribe the rendered text exactly, including every case and symbol." {
		t.Fatalf("read_file result = %+v", result)
	}
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(requests))
	}
	prompt := requests[0].Messages[0].Text()
	for _, want := range []string{
		"preserve every visible character exactly as rendered",
		"Do not correct, normalize, interpret, or replace uncertain characters",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("exact-transcription prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDescribeImage_OrdinaryPurposeKeepsDescriptionPrompt(t *testing.T) {
	t.Parallel()

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("a blue square")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("vision-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if got := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
		ImagePurpose:   "Describe the visual layout and colors.",
	}); got != "a blue square" {
		t.Fatalf("description = %q, want scripted response", got)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(requests))
	}
	wantPrompt := "Describe the visual layout and colors.\n\nBe thorough — the reader cannot see the image and will rely entirely on your description."
	if got := requests[0].Messages[0].Text(); got != wantPrompt {
		t.Fatalf("ordinary vision prompt = %q, want %q", got, wantPrompt)
	}
}

func TestVisionContract_IsPresentInComposedModelRequest(t *testing.T) {
	t.Parallel()

	sess := newSession(t, withoutGitSnapshot())
	req := sess.buildModelRequest(sess.profile, "system", []llm.Message{llm.User("inspect an image")}, sess.allToolDefinitions(0), "")
	for _, def := range req.Tools {
		if def.Name != "read_file" {
			continue
		}
		for _, want := range []string{
			"not byte-exact OCR",
			"silently normalize",
			"Do not use this output for exact-match or byte-exact transcription",
		} {
			if !strings.Contains(def.Description, want) {
				t.Errorf("read_file description missing %q:\n%s", want, def.Description)
			}
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("read_file parameters have no properties: %#v", def.Parameters)
		}
		purpose, ok := properties["purpose"].(map[string]any)
		if !ok {
			t.Fatalf("read_file parameters have no purpose property: %#v", properties)
		}
		purposeDescription, _ := purpose["description"].(string)
		for _, want := range []string{
			"transcribe the rendered text exactly",
			"cannot guarantee byte-exact output",
		} {
			if !strings.Contains(purposeDescription, want) {
				t.Errorf("read_file purpose description missing %q: %s", want, purposeDescription)
			}
		}
		return
	}
	t.Fatal("composed model request did not advertise read_file")
}

func TestVisionDescriptionSteeringStatesExactnessLimitation(t *testing.T) {
	t.Parallel()

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("a blue square")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("vision-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{
		ID:        "call-image",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"picture.png"}`),
	}}, []tool.ExecResult{{
		CallID:         "call-image",
		ToolName:       "read_file",
		Output:         "[image: picture.png]",
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	steered := sess.drainSteering()
	if len(steered) != 1 {
		t.Fatalf("steering messages = %d, want 1", len(steered))
	}
	for _, want := range []string{
		"not byte-exact OCR",
		"silently normalize",
		"Do not treat it as authoritative for exact-match or byte-exact transcription",
	} {
		if !strings.Contains(steered[0].Text, want) {
			t.Errorf("vision steering missing %q: %s", want, steered[0].Text)
		}
	}
}

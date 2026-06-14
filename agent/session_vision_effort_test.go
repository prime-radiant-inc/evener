package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// The vision side-channel builds its request manually, so it must clamp the
// reasoning effort to the model's supported levels — otherwise a top-tier alias
// like "max" reaches a model that only accepts up to "high".
func TestDescribeImage_ClampsEffortToProfileLevels(t *testing.T) {
	dir := t.TempDir()
	var gotEffort string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.ReasoningEffort != nil {
					gotEffort = *req.ReasoningEffort
				}
				return llm.Response{Message: llm.Assistant("an image of a cat")}
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)

	// Model tops out at "high" (no xhigh/max), but the session requests "max".
	profile := NewOpenAIProfile("m").WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"low", "medium", "high"}})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	desc := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("fake-png-bytes"),
		ImageMediaType: "image/png",
		ImagePurpose:   "what is in this image",
	})
	if desc == "" {
		t.Fatal("describeImage returned empty description")
	}
	if gotEffort != "high" {
		t.Fatalf("vision request effort = %q, want high (max clamped to the model's levels)", gotEffort)
	}
}

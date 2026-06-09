package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestCreateDelegateForegroundCompletesWithStructuredResult(t *testing.T) {
	var sawSchema bool
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				sawSchema = communicateOutputSchemaHasProperty(req, "summary")
				return communicateWithStructured("delegate prose", map[string]any{
					"message": "delegate prose",
					"summary": "structured summary",
					"count":   2,
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "summarize the work",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
				"count":   map[string]any{"type": "number"},
			},
			"required": []string{"message", "summary"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("job_id is empty")
	}
	if res.Type != string(jobstore.JobDelegate) || res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed delegate", res)
	}
	if !strings.HasPrefix(res.TranscriptRef, "local:") {
		t.Fatalf("transcript_ref = %q, want local ref", res.TranscriptRef)
	}
	if !strings.Contains(res.Output, "delegate prose") {
		t.Fatalf("output = %q, want prose result", res.Output)
	}
	if !res.StructuredResultValid {
		t.Fatal("structured_result_valid = false, want true")
	}
	structured, ok := res.StructuredResult.(map[string]any)
	if !ok {
		t.Fatalf("structured_result has type %T, want map", res.StructuredResult)
	}
	if structured["summary"] != "structured summary" {
		t.Fatalf("structured_result = %+v, want summary", structured)
	}
	if !sawSchema {
		t.Fatal("child communicate tool did not receive delegate result schema")
	}

	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want one delegate job", jobs)
	}
	if jobs[0].JobID != res.JobID || jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("job record = %+v, want completed job %s", jobs[0], res.JobID)
	}
}

func TestCreateDelegateBackgroundReturnsRunningJob(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithStructured("background complete", map[string]any{
					"message": "background complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run in the background",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("result = %+v, want job_id and transcript_ref", res)
	}
	if res.Type != string(jobstore.JobDelegate) ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TimedOut {
		t.Fatalf("result = %+v, want running background delegate", res)
	}

	_, _ = sess.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func newDelegateTestSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": false,
		"output":      output,
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateOutputSchemaHasProperty(req llm.Request, property string) bool {
	for _, td := range req.Tools {
		if td.Name != "communicate" {
			continue
		}
		params, ok := td.Parameters["properties"].(map[string]any)
		if !ok {
			return false
		}
		output, ok := params["output"].(map[string]any)
		if !ok {
			return false
		}
		props, ok := output["properties"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = props[property]
		return ok
	}
	return false
}

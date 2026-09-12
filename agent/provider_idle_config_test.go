package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestProviderIdleConfigPersistsAndBuildsRequest(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 10 * time.Minute}, {"45s", 45 * time.Second}} {
		var cfg SessionConfig
		if err := json.Unmarshal([]byte(`{"provider_idle_timeout":"`+tc.value+`"}`), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.applyDefaults()
		restored := configFromSnapshot(cfg.toSnapshot())
		s := &Session{cfg: restored}
		req := s.buildModelRequest(NewOpenAIProfile("gpt-5.2"), "sys", nil, nil, "")
		if req.AdapterTimeout.Request != 0 {
			t.Errorf("total deadline=%v, want disabled", req.AdapterTimeout.Request)
		}
		if req.AdapterTimeout.StreamRead != tc.want {
			t.Errorf("idle for %q=%v, want %v", tc.value, req.AdapterTimeout.StreamRead, tc.want)
		}
	}
}

func TestProviderIdleConfigRejectsInvalidDuration(t *testing.T) {
	for _, value := range []string{"-1s", "0s", "forever", "999999999999999999999h"} {
		var cfg SessionConfig
		if err := json.Unmarshal([]byte(`{"provider_idle_timeout":"`+value+`"}`), &cfg); err != nil {
			t.Fatal(err)
		}
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err == nil {
			s.Close()
			t.Errorf("accepted invalid idle duration %q", value)
		}
	}
}

func TestProviderIdleConfigResumeAndFrozenDelegate(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	frozen := SessionConfig{ProviderIdleTimeout: "45s"}.toSnapshot()
	child := subagentConfigFromFrozenDescriptor(frozen, SessionConfig{ProviderIdleTimeout: "2m"})
	if child.ProviderIdleTimeout != "45s" {
		t.Fatalf("frozen delegate timeout=%q", child.ProviderIdleTimeout)
	}
	for _, override := range []string{"", "2m", "-1s"} {
		meta := schema.SessionMeta{ID: "idle-resume", ProfileID: "openai", Model: "gpt-5.2", Config: frozen}
		restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{ProviderIdleTimeout: override, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}})
		if override == "-1s" {
			if err == nil {
				restored.Close()
				t.Fatal("restore accepted invalid duration")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := "45s"
		if override != "" {
			want = override
		}
		if got := restored.cfg.ProviderIdleTimeout; got != want {
			t.Errorf("restored idle=%q, want %q", got, want)
		}
		restored.Close()
	}
}

type visionIdleCaptureAdapter struct {
	fakeAdapter
	deadline bool
	request  llm.Request
}

func (a *visionIdleCaptureAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_, a.deadline = ctx.Deadline()
	a.request = req
	return llm.Response{Message: llm.Assistant("description")}, nil
}
func TestProviderIdleVisionUsesSessionPolicyWithoutTotalDeadline(t *testing.T) {
	adapter := &visionIdleCaptureAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withProfile(NewOpenAIProfile("gpt-5.2")), withConfig(SessionConfig{ProviderIdleTimeout: "45s"}))
	drainSessionEvents(sess)
	sess.describeImageCall(context.Background(), tool.ExecResult{ImageData: []byte("png"), ImageMediaType: "image/png"})
	if adapter.request.AdapterTimeout == nil {
		t.Fatal("vision request not captured")
	}
	if adapter.deadline {
		t.Error("vision request adds a default total context deadline")
	}
	if adapter.request.AdapterTimeout.Request != 0 {
		t.Errorf("vision total timeout=%v, want disabled", adapter.request.AdapterTimeout.Request)
	}
	if adapter.request.AdapterTimeout.StreamRead != 45*time.Second {
		t.Errorf("vision idle=%v, want configured 45s", adapter.request.AdapterTimeout.StreamRead)
	}
}

package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestSessionEnd_EmitsOncePerInputOnEveryPath pins the emit-once rule for the
// end-of-input announcement: across every path an input can leave through, the
// stream carries exactly the expected SESSION_END reasons, and a Close after the
// input announced its end adds none.
func TestSessionEnd_EmitsOncePerInputOnEveryPath(t *testing.T) {
	t.Parallel()
	final := func(text string) func(llm.Request) llm.Response {
		return func(llm.Request) llm.Response { return agenttest.FinalResponse(text) }
	}
	blocked := make(chan struct{}) // closed by the blocking adapter when its call starts
	cases := []struct {
		name  string
		setup func(c *llm.Client) SessionConfig
		drive func(t *testing.T, s *Session)
		want  []string
	}{
		{
			name: "clean settle",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{final("done")}})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				if _, err := s.ProcessInput(tripwireCtx(t), "hello", nil); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"input_complete"},
		},
		{
			name: "two sequential inputs",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{final("one"), final("two")}})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				for _, in := range []string{"first", "second"} {
					if _, err := s.ProcessInput(tripwireCtx(t), in, nil); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: []string{"input_complete", "input_complete"},
		},
		{
			name: "queued follow-up drained by the same call",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{final("one"), final("two")}})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				if err := s.Enqueue(context.Background(), "queued"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ProcessInput(tripwireCtx(t), "first", nil); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"input_complete"},
		},
		{
			name: "empty notification is a no-op",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&fakeAdapter{name: "openai"})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				if _, err := s.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
			},
			want: nil,
		},
		{
			name: "empty notification then a queued user turn",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{final("queued reply")}})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				if err := s.Enqueue(context.Background(), "queued"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"input_complete"},
		},
		{
			name: "provider failure",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&streamingAdapter{name: "openai", streamErr: llm.ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)})
				policy := llm.RetryPolicy{MaxRetries: 0}
				return SessionConfig{LLMRetryPolicy: &policy}
			},
			drive: func(t *testing.T, s *Session) {
				if _, err := s.ProcessInput(tripwireCtx(t), "hi", nil); err == nil {
					t.Fatal("expected provider failure")
				}
			},
			want: []string{"turn_failed"},
		},
		{
			name: "interrupted turn",
			setup: func(c *llm.Client) SessionConfig {
				c.Register(&blockingAdapter{name: "openai", blocked: blocked})
				return SessionConfig{}
			},
			drive: func(t *testing.T, s *Session) {
				turnCtx, cancelTurn := context.WithCancel(tripwireCtx(t))
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = s.ProcessInput(turnCtx, "hello", nil)
				}()
				<-blocked
				cancelTurn()
				<-done
			},
			want: []string{"interrupted"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			cfg := tc.setup(c)
			s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
			if err != nil {
				t.Fatal(err)
			}
			collect := drainEvents(s)
			tc.drive(t, s)
			s.Close()
			var got []string
			for _, ev := range collect() {
				if d, ok := ev.Data.(events.SessionEndData); ok && ev.Kind == events.EventSessionEnd {
					got = append(got, d.Reason)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("session end reasons = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("session end reasons = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

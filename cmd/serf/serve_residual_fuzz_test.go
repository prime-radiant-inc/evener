//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf/internal/rvreg"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
	"primeradiant.com/serf/server"
)

type residualServeServer struct {
	*server.Server
	input          chan server.InputMessage
	escalate       func(string, bool) error
	compact        func(context.Context) error
	steer          func(string)
	steerImages    func(string, []server.ImageAttachment)
	queue          func(string) error
	queueImages    func(string, []server.ImageAttachment) error
	goal           func(string) (bool, error)
	drain          func() error
	drainInput     func(string, []server.ImageAttachment) error
	promote        func(int, string) error
	cancel         func(int, string) (string, int, error)
	envelopeSource server.ThreadEnvelopeSource
	meta           func() schema.SessionMeta
	model          func(string) error
	name           func(string)
	effort         func(string)
	tasks          func() any
	jobs           func() any
	jobOutput      func(string, int64) (any, bool, error)
	clear          func(context.Context) error
	shutdown       func()
}

func newResidualServeServer(cfg server.ServerConfig) *residualServeServer {
	return &residualServeServer{Server: server.NewServer(cfg), input: make(chan server.InputMessage, 4)}
}

func (s *residualServeServer) InputCh() <-chan server.InputMessage { return s.input }
func (s *residualServeServer) SetSandboxEscalationResolveFunc(f func(string, bool) error) {
	s.escalate = f
}
func (s *residualServeServer) SetCompactFunc(f func(context.Context) error) { s.compact = f }
func (s *residualServeServer) SetSteerFunc(f func(string))                  { s.steer = f }
func (s *residualServeServer) SetSteerWithImagesFunc(f func(string, []server.ImageAttachment)) {
	s.steerImages = f
}
func (s *residualServeServer) SetQueueFunc(f func(string) error) { s.queue = f }
func (s *residualServeServer) SetQueueWithImagesFunc(f func(string, []server.ImageAttachment) error) {
	s.queueImages = f
}
func (s *residualServeServer) SetGoalFunc(f func(string) (bool, error)) { s.goal = f }
func (s *residualServeServer) SetDrainAsSteerFunc(f func() error)       { s.drain = f }
func (s *residualServeServer) SetDrainAsSteerWithInputFunc(f func(string, []server.ImageAttachment) error) {
	s.drainInput = f
}
func (s *residualServeServer) SetPromoteQueuedAsSteerFunc(f func(int, string) error) {
	s.promote = f
}
func (s *residualServeServer) SetCancelQueuedFunc(f func(int, string) (string, int, error)) {
	s.cancel = f
}
func (s *residualServeServer) SetThreadEnvelopeSource(src server.ThreadEnvelopeSource) {
	s.envelopeSource = src
}
func (s *residualServeServer) RefreshThreadEnvelope()                     {}
func (s *residualServeServer) SetModelFunc(f func(string) error)          { s.model = f }
func (s *residualServeServer) SetNameFunc(f func(string))                 { s.name = f }
func (s *residualServeServer) SetReasoningEffortFunc(f func(string))      { s.effort = f }
func (s *residualServeServer) SetTasksFunc(f func() any)                  { s.tasks = f }
func (s *residualServeServer) SetJobsFunc(f func() any)                   { s.jobs = f }
func (s *residualServeServer) SetJobOutputFunc(f func(string, int64) (any, bool, error)) {
	s.jobOutput = f
}
func (s *residualServeServer) SetClearFunc(f func(context.Context) error) { s.clear = f }
func (s *residualServeServer) SetShutdownFunc(f func())                   { s.shutdown = f }

func exerciseResidualCallbacks(s *residualServeServer) {
	ctx := context.Background()
	_ = s.escalate("missing", false)
	_ = s.compact(ctx)
	s.steer("x")
	s.steerImages("x", nil)
	_ = s.queue("queued")
	_ = s.queueImages("queued", nil)
	_, _ = s.goal(" ")
	_, _ = s.goal("objective")
	_ = s.drain()
	_ = s.drainInput("x", nil)
	_ = s.promote(0, "q_1_x")
	// Every envelope facet now enters the daemon through one seam. Exercising
	// each method keeps the residual sweep's coverage of the live producers.
	_ = s.envelopeSource.ContextPressure()
	_ = s.envelopeSource.ContextMetrics()
	_ = s.envelopeSource.DetailedStatus()
	_, _ = s.envelopeSource.ClientMutationProjection()
	_ = s.envelopeSource.TaskAggregate()
	_, _, _ = s.envelopeSource.GoalStatus()
	_, _, _ = s.envelopeSource.WorkMetrics()
	_, _ = s.envelopeSource.FailedToolCalls()
	_ = s.envelopeSource.AskPending()
	_ = s.envelopeSource.PendingEscalations()
	_, _, _ = s.envelopeSource.ReasoningInfo()
	_ = s.envelopeSource.SessionMeta()
	_ = s.model("test2")
	s.name("renamed")
	s.effort("low")
	_ = s.tasks()
	_ = s.jobs()
	_, _, _ = s.jobOutput("job_1", 1024)
}

func TestRunServeResidualCoverage(t *testing.T) {
	t.Run("resume success", func(t *testing.T) {
		t.Setenv("SERF_REASONING_EFFORT", "low")
		TestServeAsk_RestoreReportsAwaitingImmediately(t)
	})
	t.Run("resume reporting and sandbox line", func(t *testing.T) {
		var out bytes.Buffer
		reportServeResume(&out, schema.SessionMeta{ID: "id", TurnCount: 2}, cmdutil.ModelRef{Provider: "openai", Model: "new"}, "openai", "old", true)
		reportServeResume(&out, schema.SessionMeta{ID: "id", TurnCount: 2}, cmdutil.ModelRef{}, "", "", false)
		printServeSandboxLine(&out, "")
		printServeSandboxLine(&out, "sandboxed")
	})
	t.Run("default subscriber count", func(t *testing.T) {
		d := defaultServeDeps()
		s := server.NewServer(server.ServerConfig{})
		if got := d.subscriberCount(s, "missing"); got != 0 {
			t.Fatalf("subscriber count = %d", got)
		}
	})
	t.Run("escalation mapping", func(t *testing.T) {
		got := mapServePendingEscalations([]events.SandboxEscalationRequestedData{{EscalationID: "e", Tool: "shell"}})
		if len(got) != 1 || got[0].EscalationID != "e" {
			t.Fatalf("mapping = %+v", got)
		}
	})
	boom := errors.New("boom")
	base := func(t *testing.T) (serveDeps, []string) { return exactServeDeps(t), exactServeArgs(t) }
	tests := []struct {
		name   string
		mutate func(*testing.T, *serveDeps, *[]string)
	}{
		{"parse error", func(_ *testing.T, d *serveDeps, a *[]string) {
			d.newFlagSet = func(name string, _ flag.ErrorHandling) *flag.FlagSet {
				return flag.NewFlagSet(name, flag.ContinueOnError)
			}
			*a = []string{"--bad"}
		}},
		{"help", func(_ *testing.T, d *serveDeps, a *[]string) {
			d.newFlagSet = func(name string, _ flag.ErrorHandling) *flag.FlagSet {
				return flag.NewFlagSet(name, flag.ContinueOnError)
			}
			*a = []string{"-h"}
		}},
		{"seed warning", func(_ *testing.T, d *serveDeps, _ *[]string) {
			d.seedMarketplaces = func() error { return boom }
			d.listen = func(context.Context, string, string) (net.Listener, error) { return nil, boom }
		}},
		{"computed state dir", func(t *testing.T, d *serveDeps, a *[]string) {
			t.Setenv("SERF_STATE_DIR", "")
			*a = []string{"--model", "openai/test", "--dir", t.TempDir()}
			d.listen = func(context.Context, string, string) (net.Listener, error) { return nil, boom }
		}},
		{"restore error", func(_ *testing.T, d *serveDeps, a *[]string) {
			*a = append(*a, "--resume", "id")
			d.resolveMeta = func(string, string, bool) (schema.SessionMeta, error) {
				return schema.SessionMeta{ID: "id", ProfileID: "openai", Model: "test"}, nil
			}
			d.restoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
				return nil, boom
			}
		}},
		{"build profile", func(_ *testing.T, d *serveDeps, _ *[]string) {
			d.buildProfile = func(providercfg.Config, cmdutil.ModelRef, string) (*provider.Profile, error) { return nil, boom }
		}},
		{"cheap profile", func(_ *testing.T, d *serveDeps, _ *[]string) {
			d.applyCheap = func(*provider.Profile, string, *llm.Client) (*provider.Profile, error) { return nil, boom }
		}},
		{"new session", func(_ *testing.T, d *serveDeps, _ *[]string) {
			d.newSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
				return nil, boom
			}
		}},
		{"bad sandbox", func(_ *testing.T, _ *serveDeps, a *[]string) { *a = append(*a, "--sandbox", "bad") }},
		{"sandbox provision", func(_ *testing.T, d *serveDeps, _ *[]string) {
			d.provisionSandbox = func(*execenv.LocalExecutionEnvironment, *agent.SessionConfig, string) error { return boom }
		}},
		{"bad effort", func(_ *testing.T, _ *serveDeps, a *[]string) { *a = append(*a, "--reasoning-effort", "bad") }},
		{"cpu success", func(_ *testing.T, d *serveDeps, a *[]string) {
			*a = append(*a, "--cpu-profile", "x")
			d.startCPUProfile = func(string) (func(), error) { return func() {}, nil }
			d.listen = func(context.Context, string, string) (net.Listener, error) { return nil, boom }
		}},
		{"trace success", func(_ *testing.T, d *serveDeps, a *[]string) {
			*a = append(*a, "--trace", "x")
			d.startTrace = func(string) (func(), error) { return func() {}, nil }
			d.listen = func(context.Context, string, string) (net.Listener, error) { return nil, boom }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, args := base(t)
			tc.mutate(t, &d, &args)
			if err := runServeWithDeps(args, d); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("callbacks and input", func(t *testing.T) {
		d, args := base(t)
		var captured *residualServeServer
		d.newServer = func(cfg server.ServerConfig) serveServer { captured = newResidualServeServer(cfg); return captured }
		d.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
			onDrained()
		}
		d.subscriberCount = func(serveServer, string) int { return 1 }
		d.observeCallbacks = func(c serveCallbackObserver) {
			c.notify()
			if c.subscriberCount() != 1 {
				t.Fatal("subscriber callback")
			}
			_ = c.pendingEscalations()
			c.setSession(nil)
			c.setSession(c.session)
		}
		d.register = func(*rvreg.Registration, string, rendezvous.Entry) error { return boom }
		d.serveHTTP = func(*http.Server, net.Listener) error {
			exerciseResidualCallbacks(captured)
			captured.input <- server.InputMessage{Text: "hello", Kind: agent.EntryUserInput}
			close(captured.input)
			time.Sleep(20 * time.Millisecond)
			_ = captured.clear(context.Background())
			captured.shutdown()
			return http.ErrServerClosed
		}
		if err := runServeWithDeps(append(args, "--verbose"), d); err != nil {
			t.Fatal(err)
		}
	})

	for _, clearCase := range []struct {
		name   string
		mutate func(*serveDeps)
	}{
		{"clear provision error", func(d *serveDeps) {
			calls := 0
			d.provisionSandbox = func(*execenv.LocalExecutionEnvironment, *agent.SessionConfig, string) error {
				calls++
				if calls > 1 {
					return boom
				}
				return nil
			}
		}},
		{"clear session error", func(d *serveDeps) {
			d.newClearSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
				return nil, boom
			}
		}},
		// The daemon prepares an identity twice: once at startup, once per
		// clear. Only the clear-time one may fail here -- a startup failure
		// aborts runServe before there is a clear callback to drive.
		{"clear prepare error", func(d *serveDeps) {
			prepare := d.prepareAppIdentity
			calls := 0
			d.prepareAppIdentity = func(sourceID, threadID, transcriptPath string) (server.PreparedAppIdentity, error) {
				calls++
				if calls > 1 {
					return server.PreparedAppIdentity{}, boom
				}
				return prepare(sourceID, threadID, transcriptPath)
			}
		}},
		{"clear rendezvous error", func(d *serveDeps) { d.updateSessionID = func(*rvreg.Registration, string) error { return boom } }},
	} {
		t.Run(clearCase.name, func(t *testing.T) {
			d, args := base(t)
			var captured *residualServeServer
			d.newServer = func(cfg server.ServerConfig) serveServer { captured = newResidualServeServer(cfg); return captured }
			d.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
				onDrained()
			}
			clearCase.mutate(&d)
			d.serveHTTP = func(*http.Server, net.Listener) error {
				_ = captured.clear(context.Background())
				captured.shutdown()
				return http.ErrServerClosed
			}
			if err := runServeWithDeps(args, d); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("http error and environment fallbacks", func(t *testing.T) {
		t.Setenv("SERF_STATE_DIR", t.TempDir())
		t.Setenv("SERF_RUN_DIR", "")
		t.Setenv("SERF_HUB_SPAWNED", "1")
		d, args := base(t)
		args = []string{"--model", "openai/test", "--dir", t.TempDir()}
		d.register = func(*rvreg.Registration, string, rendezvous.Entry) error { return boom }
		d.serveHTTP = func(*http.Server, net.Listener) error { return boom }
		if err := runServeWithDeps(args, d); !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
	})
}

func FuzzServeResidualCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) { TestRunServeResidualCoverage(t) })
}

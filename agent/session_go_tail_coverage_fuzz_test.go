//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

type tailCoverageClock struct {
	clock.Clock
	mu    sync.Mutex
	funcs []func()
}

func newTailCoverageClock() *tailCoverageClock {
	return &tailCoverageClock{Clock: clock.Real()}
}

func (c *tailCoverageClock) AfterFunc(_ time.Duration, fn func()) clock.Timer {
	c.mu.Lock()
	c.funcs = append(c.funcs, fn)
	c.mu.Unlock()
	return tailCoverageTimer{}
}

func (c *tailCoverageClock) fire(i int) {
	c.mu.Lock()
	fn := c.funcs[i]
	c.mu.Unlock()
	fn()
}

type tailCoverageTimer struct{}

func (tailCoverageTimer) C() <-chan time.Time      { return nil }
func (tailCoverageTimer) Stop() bool               { return true }
func (tailCoverageTimer) Reset(time.Duration) bool { return true }

type tailCoverageAdapter struct{}

func (tailCoverageAdapter) Name() string { return "google" }
func (tailCoverageAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Assistant("offline result")}, nil
}
func (tailCoverageAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type tailCoverageSandboxEnv struct {
	execenv.ExecutionEnvironment
	wrapper *sandbox.Wrapper
}

func (e tailCoverageSandboxEnv) KernelWrapper() *sandbox.Wrapper { return e.wrapper }

type tailCoverageBlockingLister struct {
	tailCoverageAdapter
	entered chan struct{}
	release chan struct{}
}

func (a *tailCoverageBlockingLister) Name() string { return "openai" }
func (a *tailCoverageBlockingLister) ListModels(context.Context) ([]llm.ModelInfo, error) {
	close(a.entered)
	<-a.release
	return nil, nil
}

func FuzzSessionGoTailCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		t.Run("notification retry branches", func(t *testing.T) {
			clk := newTailCoverageClock()
			s := &Session{clock: clk}

			s.pendingJobNotifsMu.Lock()
			s.scheduleJobNotificationRetryLocked()
			s.jobNotifyRetry.generation++
			s.pendingJobNotifsMu.Unlock()
			clk.fire(0)

			s.pendingJobNotifsMu.Lock()
			s.jobNotifyRetry.active = false
			s.scheduleJobNotificationRetryLocked()
			s.pendingJobNotifsMu.Unlock()
			clk.fire(1)
			if s.jobNotifyRetry.delay != jobNotificationRetryInitialDelay {
				t.Fatalf("empty retry delay = %v", s.jobNotifyRetry.delay)
			}

			s.enqueueJobNotification(jobNotification{JobID: "pending"})
			s.resetJobNotificationRetry()
		})

		t.Run("provider tool transitions", func(t *testing.T) {
			client := llm.NewClient()
			client.Register(tailCoverageAdapter{})
			s := &Session{
				reg:        tool.NewRegistry(),
				env:        execenv.NewLocalExecutionEnvironment(t.TempDir()),
				profile:    newGeminiProfile("gemini"),
				client:     client,
				httpClient: nil,
			}
			s.reapplyProviderSpecificTools("openai", "google")
			web := s.reg.Get("web_search")
			if web == nil {
				t.Fatal("google transition did not register web_search")
			}
			got, err := web.Exec(context.Background(), s.env, map[string]any{"query": "offline"})
			if err != nil || got != "offline result" {
				t.Fatalf("offline web_search = %v, %v", got, err)
			}
			netOff := false
			policy, err := sandbox.Resolve(
				sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: &netOff},
				sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true},
				t.TempDir(),
			)
			if err != nil {
				t.Fatalf("resolve offline sandbox: %v", err)
			}
			wrapper, err := sandbox.NewWrapper(policy, "/bin/true", t.TempDir())
			if err != nil {
				t.Fatalf("new offline wrapper: %v", err)
			}
			_, err = web.Exec(context.Background(), tailCoverageSandboxEnv{ExecutionEnvironment: s.env, wrapper: wrapper}, map[string]any{"query": "denied"})
			if err == nil {
				t.Fatal("network-off web_search unexpectedly succeeded")
			}
			s.reapplyProviderSpecificTools("google", "openai")
			if s.reg.Get("web_search") != nil {
				t.Fatal("leaving google retained web_search")
			}
		})

		t.Run("set model guards and switch", func(t *testing.T) {
			s := &Session{profile: NewOpenAIProfile("gpt-5"), reg: tool.NewRegistry()}
			s.resolveProfile = func(string) (*provider.Profile, error) {
				return nil, errors.New("resolver failure")
			}
			s.SetModel("google/gemini")

			s.resolveProfile = func(string) (*provider.Profile, error) {
				return newGeminiProfile("gemini"), nil
			}
			s.cfg.testOnly.minimalSystemPrompt = true
			s.SetModel("google/gemini")
			if s.profile.BehaviorTag() != "google" || s.reg.Get("web_search") == nil {
				t.Fatalf("cross-provider switch = %q, web_search=%v", s.profile.BehaviorTag(), s.reg.Get("web_search") != nil)
			}

			lister := &tailCoverageBlockingLister{entered: make(chan struct{}), release: make(chan struct{})}
			client := llm.NewClient()
			client.Register(lister)
			tracing := &Session{profile: NewOpenAIProfile("gpt-5"), client: client, reg: tool.NewRegistry()}
			done := make(chan struct{})
			go func() {
				tracing.SetModel("gpt-5.1")
				close(done)
			}()
			<-lister.entered
			tracing.mu.Lock()
			tracing.closing = true
			tracing.mu.Unlock()
			close(lister.release)
			<-done
			if tracing.profile.Model() != "gpt-5" {
				t.Fatalf("closing session changed model to %q", tracing.profile.Model())
			}
		})

		t.Run("rename and metadata defensive branches", func(t *testing.T) {
			s := &Session{profile: NewOpenAIProfile("gpt-5")}
			s.Rename("   ")
			s.applyModelRequestMetadata(s.profile, nil)
		})

		t.Run("transcript warning branches", func(t *testing.T) {
			fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
			w, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{})
			if err != nil {
				t.Fatalf("NewWriterWithFS: %v", err)
			}
			fs.fail = true
			s := &Session{transcript: w, transcriptReady: true, events: make(chan events.SessionEvent, 8), clock: newTailCoverageClock()}
			s.appendTurn(schema.TurnUserInput, llm.User("append"))
			if err := s.appendSteeringTurnDurably("durable", ""); err == nil {
				t.Fatal("durable append unexpectedly succeeded")
			}
			if err := s.appendAssistantTurn(llm.Response{Message: llm.Assistant("assistant")}, ModelAttemptMetadata{}); !errors.Is(err, errInjectedTranscriptWrite) {
				t.Fatalf("assistant append error = %v, want injected transcript write failure", err)
			}
			if got := len(s.events); got != 3 {
				t.Fatalf("warning events = %d, want 3", got)
			}
		})

		t.Run("auto save warning", func(t *testing.T) {
			blocker := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
				t.Fatalf("write blocker: %v", err)
			}
			profile := NewOpenAIProfile("gpt-5")
			s := &Session{
				id:         "autosave",
				stateDir:   blocker,
				profile:    profile,
				contextMgr: contextmgr.NewManager(profile, nil),
				clock:      newTailCoverageClock(),
				events:     make(chan events.SessionEvent, 1),
			}
			s.maybeAutoSave()
			if got := len(s.events); got != 1 {
				t.Fatalf("auto-save warnings = %d, want 1", got)
			}
		})
	})
}

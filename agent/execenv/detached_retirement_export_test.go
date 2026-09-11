//go:build linux || darwin

package execenv

import (
	"context"
	"os"
	"slices"
	"sync"
)

// These exports exist only in the execenv test binary. The external test imports
// agent, which imports this augmented package, without a production API or cycle.
type DetachedRetirementEnvironmentForTest struct {
	*LocalExecutionEnvironment
	observer *DetachedRetirementObserverForTest
}

type DetachedRetirementObserverForTest struct {
	mu       sync.Mutex
	env      *LocalExecutionEnvironment
	commands []*detachedRetirementRuntimeForTest
}

type DetachedRetirementSnapshotForTest struct {
	Runtime        *detachedRetirementRuntimeForTest
	Process        *os.Process
	Receipt        DetachedProcess
	Signals        []string
	Managed        bool
	CombinedOutput bool
}

type detachedRetirementFactoryForTest struct {
	observer *DetachedRetirementObserverForTest
}

type detachedRetirementRuntimeForTest struct {
	commandRuntime
	actual         *systemCommandRuntime
	observer       *DetachedRetirementObserverForTest
	process        *os.Process
	receipt        DetachedProcess
	signals        []string
	combinedOutput bool
}

func NewDetachedRetirementObserverForTest(root string) (*DetachedRetirementEnvironmentForTest, *DetachedRetirementObserverForTest) {
	env := NewLocalExecutionEnvironment(root)
	observer := &DetachedRetirementObserverForTest{env: env}
	env.commandFactory = detachedRetirementFactoryForTest{observer: observer}
	return &DetachedRetirementEnvironmentForTest{LocalExecutionEnvironment: env, observer: observer}, observer
}

func (f detachedRetirementFactoryForTest) wrap(next commandRuntime) commandRuntime {
	wrapped := &detachedRetirementRuntimeForTest{commandRuntime: next, actual: next.(*systemCommandRuntime), observer: f.observer}
	f.observer.mu.Lock()
	f.observer.commands = append(f.observer.commands, wrapped)
	f.observer.mu.Unlock()
	return wrapped
}

func (f detachedRetirementFactoryForTest) Shell(command string) commandRuntime {
	return f.wrap(systemCommandRuntimeFactory{}.Shell(command))
}
func (f detachedRetirementFactoryForTest) Argv(name string, args ...string) commandRuntime {
	return f.wrap(systemCommandRuntimeFactory{}.Argv(name, args...))
}

func (r *detachedRetirementRuntimeForTest) Configure(config commandRuntimeConfig) {
	r.commandRuntime.Configure(config)
	r.observer.mu.Lock()
	r.combinedOutput = config.CombinedOutput != nil
	r.observer.mu.Unlock()
}
func (r *detachedRetirementRuntimeForTest) Start() error {
	err := r.commandRuntime.Start()
	if err == nil {
		r.observer.mu.Lock()
		r.process = r.actual.cmd.Process
		r.observer.mu.Unlock()
	}
	return err
}
func (r *detachedRetirementRuntimeForTest) Terminate() {
	r.observer.mu.Lock()
	r.signals = append(r.signals, "terminate")
	r.observer.mu.Unlock()
	r.commandRuntime.Terminate()
}
func (r *detachedRetirementRuntimeForTest) Kill() {
	r.observer.mu.Lock()
	r.signals = append(r.signals, "kill")
	r.observer.mu.Unlock()
	r.commandRuntime.Kill()
}
func (r *detachedRetirementRuntimeForTest) SignalName() string {
	return cmdSignalName(r.commandRuntime)
}

func (e *DetachedRetirementEnvironmentForTest) DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error) {
	receipt, err := e.LocalExecutionEnvironment.DetachCommand(ctx, command, workingDir, envVars)
	if err == nil {
		e.observer.mu.Lock()
		for _, runtime := range e.observer.commands {
			if runtime.process != nil && runtime.process.Pid == receipt.PID {
				runtime.receipt = receipt
				break
			}
		}
		e.observer.mu.Unlock()
	}
	return receipt, err
}

func (o *DetachedRetirementObserverForTest) Snapshot() []DetachedRetirementSnapshotForTest {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []DetachedRetirementSnapshotForTest
	for _, runtime := range o.commands {
		if runtime.receipt.Done == nil {
			continue
		}
		_, managed := o.env.runningPIDs.Load(runtime.receipt.PID)
		out = append(out, DetachedRetirementSnapshotForTest{Runtime: runtime, Process: runtime.process, Receipt: runtime.receipt, Signals: slices.Clone(runtime.signals), Managed: managed, CombinedOutput: runtime.combinedOutput})
	}
	return out
}

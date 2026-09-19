package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/internal/interactiveartifacts"
	"primeradiant.com/evener/internal/plugins"
)

type brokerSeamManagedRuntime struct{}

func (brokerSeamManagedRuntime) Catalog() ([]agent.ManagedTool, error) { return nil, nil }
func (brokerSeamManagedRuntime) Bind(agent.ManagedSession) (agent.ManagedBinding, error) {
	return nil, nil
}

func TestOpenInheritedDaemonBrokerMarksDescriptorsCloseOnExec(t *testing.T) {
	daemonRead, hubWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	hubRead, daemonWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hubRead.Close()
	defer hubWrite.Close()

	launchID := interactiveartifacts.NewBrokerID()
	writeDone := make(chan error, 1)
	go func() { writeDone <- interactiveartifacts.WriteLaunchCorrelation(hubWrite, launchID) }()

	broker, err := openInheritedDaemonBroker(int(daemonRead.Fd()), int(daemonWrite.Fd()))
	if err != nil {
		t.Fatalf("open inherited broker: %v", err)
	}
	defer broker.Close()
	if err := <-writeDone; err != nil {
		t.Fatalf("write launch correlation: %v", err)
	}

	for _, fd := range []int{int(daemonRead.Fd()), int(daemonWrite.Fd())} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Fatalf("read descriptor %d flags: %v", fd, err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Errorf("descriptor %d is inheritable after broker initialization", fd)
		}
	}
}

func TestServePassesInheritedBrokerRuntimeIntoSessionConstruction(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	daemonRead, hubWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	hubRead, daemonWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hubRead.Close()
	defer hubWrite.Close()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- interactiveartifacts.WriteLaunchCorrelation(hubWrite, interactiveartifacts.NewBrokerID())
	}()

	wantRuntime := brokerSeamManagedRuntime{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.resolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{}, nil
	}
	deps.managedRuntime = func(broker *interactiveartifacts.DaemonBroker) agent.ManagedRuntimeProvider {
		if broker == nil {
			t.Fatal("managed-runtime factory received nil broker")
		}
		return wantRuntime
	}
	deps.provisionSandbox = func(_ *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		if cfg.ManagedRuntime != wantRuntime {
			t.Fatalf("managed runtime = %#v, want inherited broker runtime", cfg.ManagedRuntime)
		}
		return errors.New("stop after managed-runtime seam")
	}

	err = runServeWithDeps([]string{
		"--model", "openai/gpt-test",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--artifact-broker-read-fd", strconv.Itoa(int(daemonRead.Fd())),
		"--artifact-broker-write-fd", strconv.Itoa(int(daemonWrite.Fd())),
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "stop after managed-runtime seam") {
		t.Fatalf("serve error = %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write launch correlation: %v", err)
	}
}

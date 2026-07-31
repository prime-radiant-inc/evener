package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubstart"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
)

type tuiProgram interface {
	Run() (tea.Model, error)
	Send(tea.Msg)
}

var (
	exitProcess                     = os.Exit
	processArgs                     = func() []string { return os.Args }
	processExecutable               = os.Executable
	processGetenv                   = os.Getenv
	standardError         io.Writer = os.Stderr
	standardOutput        io.Writer = os.Stdout
	parseStartupOptions             = hubstart.ParseTUIStartupOptions
	ensureUserConfigDirs            = cmdutil.EnsureUserConfigDirs
	warmModelCatalog                = func() { llm.EmbeddedModelCatalog() }
	startHubClient                  = hubstart.StartHubClient
	probeTerminalDefaults           = tuitheme.ProbeTerminalDefaults
	initThemeFromStateDir           = tuitheme.InitThemeFromStateDir
	applyTerminalBg                 = tuitheme.ApplyTerminalBg
	resetTerminalBg                 = tuitheme.ResetTerminalBg
	newTUIProgram                   = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		return tea.NewProgram(model, opts...)
	}
)

func main() {
	// All shutdown work goes through run() so deferred cleanup (notably
	// tuitheme.ResetTerminalBg, which restores OSC 10/11 colors the user expected
	// before we started) actually fires on every exit path. Calling
	// os.Exit directly from main would skip those defers.
	exitProcess(run())
}

func run() int {
	args := processArgs()
	if len(args) > 0 {
		args = args[1:]
	}
	startupOpts, err := parseStartupOptions(args, processGetenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage has already been printed by the flag package via fs.Usage.
			return 0
		}
		_, _ = fmt.Fprintf(standardError, "serf-tui: %v\n", err)
		return 2
	}
	if err := ensureUserConfigDirs(); err != nil {
		_, _ = fmt.Fprintf(standardError, "serf-tui: %v\n", err)
		return 1
	}

	// Warm the embedded model catalog (a sync.Once-memoized ~1.58MB JSON parse)
	// concurrently with the hub-connect round trip below, so the model picker's
	// first fetch never pays that cost on the critical path to user input.
	go warmModelCatalog()

	ctx := context.Background()
	// The feed has to exist before the connection does: it becomes the client's
	// ordered frame handler, which only takes effect when installed ahead of
	// the receive loop.
	frames := newHubFrameFeed()
	runtime, err := startHubClient(ctx, hubstart.HubStartConfig{
		RawAddr:           startupOpts.HubAddr,
		HubBin:            startupOpts.HubBin,
		StateDir:          startupOpts.StateDir,
		LogFile:           startupOpts.LogFile,
		AuthToken:         startupOpts.AuthToken,
		CurrentExecutable: currentExecutable(),
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
		ObserveFrames:     frames.Observe,
	})
	if err != nil {
		_, _ = fmt.Fprint(standardError, hubstart.StartupErrorScreen(err))
		return 1
	}

	// Probe the terminal's default fg/bg BEFORE any tuitheme.SetTheme call, so
	// (a) "system" theme detection has cached probe data and (b) the
	// deferred restore on exit can return the exact originals.
	probeTerminalDefaults()
	initThemeFromStateDir(startupOpts.StateDir)
	applyTerminalBg()
	defer resetTerminalBg()

	frames.SetTransportCloser(runtime.Client.Close)
	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	m.frames = frames
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	program := newTUIProgram(m, programOpts...)
	if m.pending != nil {
		m.pending.SetSend(program.Send)
	}
	finalModel, err := program.Run()
	if err != nil {
		_, _ = fmt.Fprintf(standardError, "serf-tui: %v\n", err)
		return 1
	}
	if message := postQuitMessageFromModel(finalModel); message != "" {
		_, _ = fmt.Fprintln(standardOutput, message)
	}
	return 0
}

func postQuitMessageFromModel(model tea.Model) string {
	m, ok := model.(hubModel)
	if !ok {
		return ""
	}
	return strings.TrimSpace(m.postQuitMessage)
}

// currentExecutable returns the absolute path of the running serf-tui
// binary. It prefers os.Executable() (always absolute on supported
// platforms) and falls back to os.Args[0] when the OS cannot report a
// path. Returning the absolute path lets binresolve.Resolve locate a
// sibling serf-hub even when serf-tui was launched via a relative path
// like "./serf-tui" — which would otherwise be rejected by exec.ErrDot.
func currentExecutable() string {
	if exe, err := processExecutable(); err == nil && exe != "" {
		return exe
	}
	if args := processArgs(); len(args) > 0 {
		return args[0]
	}
	return ""
}

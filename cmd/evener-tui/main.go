package tui

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
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubstart"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
	"primeradiant.com/evener/cmdutil"
)

type tuiProgram interface {
	Run() (tea.Model, error)
	Send(tea.Msg)
}

var (
	exitProcess                     = os.Exit //nolint:unused // swapped in fuzz tests (main_fuzz_test.go)
	processArgs                     = func() []string { return os.Args }
	processExecutable               = os.Executable
	processGetenv                   = os.Getenv
	standardError         io.Writer = os.Stderr
	standardOutput        io.Writer = os.Stdout
	parseStartupOptions             = hubstart.ParseTUIStartupOptions
	ensureUserConfigDirs            = cmdutil.EnsureUserConfigDirs
	startHubClient                  = hubstart.StartHubClient
	probeTerminalDefaults           = tuitheme.ProbeTerminalDefaults
	initThemeFromStateDir           = tuitheme.InitThemeFromStateDir
	applyTerminalBg                 = tuitheme.ApplyTerminalBg
	resetTerminalBg                 = tuitheme.ResetTerminalBg
	newTUIProgram                   = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		return tea.NewProgram(model, opts...)
	}
)

// Run is the library entry point used by the `evener tui` subcommand. It
// temporarily installs args/stdout/stderr onto the package-level swappable
// vars that run() and the tests already use, so the existing test hooks
// (processArgs, standardError, standardOutput, exitProcess) keep working
// unchanged.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	oldArgs, oldOut, oldErr := processArgs, standardOutput, standardError
	processArgs = func() []string { return append([]string{"evener-tui"}, args...) }
	standardOutput = stdout
	standardError = stderr
	defer func() {
		processArgs = oldArgs
		standardOutput = oldOut
		standardError = oldErr
	}()
	return run()
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
		_, _ = fmt.Fprintf(standardError, "evener-tui: %v\n", err)
		return 2
	}
	if err := ensureUserConfigDirs(); err != nil {
		_, _ = fmt.Fprintf(standardError, "evener-tui: %v\n", err)
		return 1
	}

	ctx := context.Background()
	hubConfig := hubstart.HubStartConfig{
		RawAddr:           startupOpts.HubAddr,
		HubBin:            startupOpts.HubBin,
		StateDir:          startupOpts.StateDir,
		LogFile:           startupOpts.LogFile,
		AuthToken:         startupOpts.AuthToken,
		CurrentExecutable: currentExecutable(),
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
	}
	// Every connection is opened the same way, including the ones that replace
	// a dead one: same address, same auth, same autostart — which is what
	// brings back a hub that exited rather than merely blipped. The feed has to
	// exist before the connection does, because it becomes the client's ordered
	// frame handler and that only takes effect installed ahead of the receive
	// loop.
	dialHub := func(ctx context.Context) (hubstart.HubRuntime, *hubFrameFeed, error) {
		frames := newHubFrameFeed()
		config := hubConfig
		config.ObserveFrames = frames.Observe
		runtime, err := startHubClient(ctx, config)
		if err != nil {
			return hubstart.HubRuntime{}, nil, err
		}
		frames.SetTransportCloser(runtime.Client.Close)
		return runtime, frames, nil
	}
	runtime, frames, err := dialHub(ctx)
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

	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	m.frames = frames
	m.dialHub = func(ctx context.Context) (*appwire.Client, *hubFrameFeed, error) {
		replacement, frames, err := dialHub(ctx)
		return replacement.Client, frames, err
	}
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
		_, _ = fmt.Fprintf(standardError, "evener-tui: %v\n", err)
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

// currentExecutable returns the absolute path of the running evener-tui
// binary. It prefers os.Executable() (always absolute on supported
// platforms) and falls back to os.Args[0] when the OS cannot report a
// path. Returning the absolute path lets binresolve.Resolve locate a
// sibling evener-hub even when evener-tui was launched via a relative path
// like "./evener-tui" — which would otherwise be rejected by exec.ErrDot.
func currentExecutable() string {
	if exe, err := processExecutable(); err == nil && exe != "" {
		return exe
	}
	if args := processArgs(); len(args) > 0 {
		return args[0]
	}
	return ""
}

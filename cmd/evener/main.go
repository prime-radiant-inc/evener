package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/buildinfo"
	doctorcmd "primeradiant.com/evener/cmd/evener-doctor"
	hubcmd "primeradiant.com/evener/cmd/evener-hub"
	migratecmd "primeradiant.com/evener/cmd/evener-migrate"
	"primeradiant.com/evener/cmd/evener/internal/cliprompt"
	"primeradiant.com/evener/cmd/evener/internal/launchcheck"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
)

// Alias for brevity within flag definitions.
type stringSliceFlag = cmdutil.StringSliceFlag

type runCLIFlags struct {
	*launchFlags
	resumeWith            *string
	listSessions          *bool
	noDefaultMarketplaces *bool
	runTimeout            *time.Duration
	askResponder          *string
}

func main() {
	mainWithDeps(defaultMainDeps())
}

type mainDeps struct {
	args       []string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	stdinMode  func() (os.FileMode, error)
	exit       func(int)
	dispatch   func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error)
	startCPU   func(string) (func(), error)
	startTrace func(string) (func(), error)
	notify     func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	run        func(context.Context, runConfig) error
}

func defaultMainDeps() mainDeps {
	return defaultMainDepsWithStdin(os.Stdin)
}

func defaultMainDepsWithStdin(stdin *os.File) mainDeps {
	return mainDeps{
		args: os.Args[1:], stdin: stdin, stdout: os.Stdout, stderr: os.Stderr,
		stdinMode: func() (os.FileMode, error) {
			stat, err := stdin.Stat()
			if stat == nil {
				return 0, err
			}
			return stat.Mode(), err
		},
		exit: os.Exit, dispatch: dispatchCLICommand,
		startCPU: cmdutil.StartCPUProfile, startTrace: cmdutil.StartTrace,
		notify: notifyRunContext, run: runWithScratchReclaim,
	}
}

// notifyRunContext installs the one-shot run's signal handling on a SINGLE
// channel and derives both behaviors from the delivery count: the first
// SIGINT/SIGTERM cancels ctx so the run closes gracefully — flushing the
// transcript and writing --export-atif — and a repeat exits with 128+signal so
// a close wedged on a provider or lock stays endable by the signal harnesses
// send, not only by SIGKILL. The returned cancel stops the handler and ends the
// goroutine, so a caller that finishes normally leaves no global signal handler
// behind.
//
// One channel, not two, is load-bearing. signal.NotifyContext keeps its
// registration in place until its cancel runs, so a second channel for the
// escalation races it for each delivery: whichever was armed first consumes it,
// and the other either loses the cancellation (no graceful close) or starts its
// own count at one (the repeat is swallowed). Counting deliveries on one
// channel has no such window: a signal is always both the cancellation and the
// escalation's first delivery.
func notifyRunContext(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, len(signals))
	signal.Notify(ch, signals...)
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		select {
		case <-stop:
			return
		case <-ch:
			cancel()
		}
		select {
		case <-stop:
		case sig := <-ch:
			os.Exit(exitCodeForSignal(sig))
		}
	}()
	return ctx, func() {
		cancel()
		stopOnce.Do(func() {
			signal.Stop(ch)
			close(stop)
		})
	}
}

// exitCodeForSignal mirrors the shell convention for a process ended by a
// signal (128 + number), so an escalated exit is distinguishable from the
// generic failure code.
func exitCodeForSignal(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

func mainWithDeps(deps mainDeps) {
	// Report the evener build version in agent session metadata. The provider
	// User-Agent is stamped by cmdutil.NewRegistryClient when the client loads.
	agent.BuildVersion = buildinfo.Version()

	// Quick flags that don't need full flag.Parse().
	if len(deps.args) > 0 && deps.args[0] == "--version" {
		_, _ = fmt.Fprintln(deps.stdout, "evener", buildinfo.VersionLong())
		return
	}

	// An inherited TMPDIR can name a session temp container that has already
	// been deleted. Nothing below may inherit it. Placed after the version
	// query, which needs no temp dir.
	if !cmdutil.TempDirExists(envvars.TmpDir.Getenv()) {
		_ = envvars.TmpDir.Unsetenv()
	}

	// Subcommand dispatch — before flag.Parse() so subcommands get their own flag sets.
	if handled, label, err := deps.dispatch(deps.args, deps.stdin, deps.stdout, deps.stderr); handled {
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				// Subcommand printed usage via fs.Usage; exit cleanly.
				return
			}
			// subcommandExit carries the exit code from a library-style
			// subcommand (Run returning a non-zero int). The subcommand
			// already printed its own diagnostics; propagate the code.
			if code, ok := errors.AsType[subcommandExitError](err); ok {
				if code != 0 {
					deps.exit(int(code))
				}
				return
			}
			_, _ = fmt.Fprintf(deps.stderr, "%s: %v\n", label, err)
			deps.exit(1)
			return
		}
		return
	}

	fs, flags := newRunFlagSet(deps.stderr)
	if err := fs.Parse(deps.args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		deps.exit(2)
		return
	}
	if err := rejectPluginSelectionWithResume(flags.enabledPlugins.Value(), *flags.resume, *flags.resumeLast); err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "evener: %v\n", err)
		deps.exit(2)
		return
	}
	if err := rejectAskResponderWithResume(*flags.askResponder, *flags.resume, *flags.resumeWith, *flags.resumeLast); err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "evener: %v\n", err)
		deps.exit(2)
		return
	}

	var cpuStop func()
	if *flags.cpuProfile != "" {
		stop, err := deps.startCPU(*flags.cpuProfile)
		if err != nil {
			_, _ = fmt.Fprintf(deps.stderr, "evener: %v\n", err)
			deps.exit(1)
			return
		}
		cpuStop = stop
	}
	var traceStop func()
	if *flags.traceFile != "" {
		stop, err := deps.startTrace(*flags.traceFile)
		if err != nil {
			_, _ = fmt.Fprintf(deps.stderr, "evener: %v\n", err)
			deps.exit(1)
			return
		}
		traceStop = stop
	}
	if cpuStop != nil {
		defer cpuStop()
	}
	if traceStop != nil {
		defer traceStop()
	}

	isResume := *flags.resume != "" || *flags.resumeWith != "" || *flags.resumeLast || *flags.listSessions
	mode, _ := deps.stdinMode()
	stdinIsCharDevice := (mode & os.ModeCharDevice) != 0
	prompt := cliprompt.Read(fs.Args(), *flags.listSessions, deps.stdin, stdinIsCharDevice)

	if prompt == "" && !isResume {
		if len(deps.args) == 0 && stdinIsCharDevice {
			printStartupUsage(deps.stderr)
		} else {
			fs.Usage()
		}
		deps.exit(1)
		return
	}
	if *flags.runTimeout < 0 {
		_, _ = fmt.Fprintf(deps.stderr, "evener: invalid --timeout %s: must be non-negative\n", flags.runTimeout.String())
		deps.exit(2)
		return
	}

	// SIGTERM is registered alongside SIGINT: a campaign harness that kills a
	// run at its wall deadline sends SIGTERM, and the default disposition would
	// terminate the process before the deferred sess.Close() ran. That close is
	// where the transcript is flushed and --export-atif writes the trajectory,
	// so ignoring SIGTERM loses exactly the record a timed-out run needs. Every
	// other entry point (serve, evener-hub, attach) already listens for both.
	ctx, cancel := deps.notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := deps.run(ctx, runConfig{
		prompt:                      prompt,
		model:                       *flags.model,
		fastCheapModel:              *flags.fastCheapModel,
		visionModel:                 *flags.visionModel,
		workDir:                     *flags.workDir,
		stateDir:                    *flags.stateDir,
		disableMemory:               *flags.disableMemory,
		systemPrompt:                *flags.systemPrompt,
		systemPromptAppend:          []string(flags.systemPromptAppend),
		maxRounds:                   *flags.maxRounds,
		maxSubagentDepth:            *flags.maxSubagentDepth,
		maxConcurrentDelegates:      *flags.maxConcurrentDelegates,
		maxRetainedTerminal:         *flags.maxRetainedTerminal,
		shareTaskStore:              *flags.shareTaskStore,
		resultToolName:              *flags.resultToolName,
		reasoningEffort:             *flags.reasoningEffort,
		contextStrategy:             *flags.contextStrategy,
		exportATIF:                  *flags.exportATIF,
		exportATIFProviderHandles:   *flags.exportATIFProviderHandles,
		outputSchema:                *flags.outputSchema,
		verbose:                     *flags.verbose,
		agentName:                   *flags.agentName,
		skillsDirs:                  []string(flags.skillsDirs),
		mcpServers:                  []string(flags.mcpServers),
		mcpConfigs:                  []string(flags.mcpConfigs),
		pluginDirs:                  []string(flags.pluginDirs),
		enabledPlugins:              flags.enabledPlugins.Value(),
		noDefaultMarketplaces:       *flags.noDefaultMarketplaces,
		systemPromptAsUser:          *flags.systemPromptAsUser,
		openAIResponsesContinuation: *flags.openAIResponsesContinuation,
		providerIdleTimeout:         *flags.providerIdleTimeout,
		sandboxMode:                 *flags.sandbox,
		sandboxNet:                  *flags.sandboxNet,
		apiLog:                      *flags.apiLog,
		runTimeout:                  *flags.runTimeout,
		askResponder:                *flags.askResponder,
		stdout:                      deps.stdout,
		stderr:                      deps.stderr,
		resume:                      *flags.resume,
		resumeWith:                  *flags.resumeWith,
		resumeLast:                  *flags.resumeLast,
		listSessions:                *flags.listSessions,
	})
	if err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "evener: %v\n", err)
		cancel()
		deps.exit(1)
		return
	}
}

func newRunFlagSet(stderr io.Writer) (*flag.FlagSet, *runCLIFlags) {
	fs := flag.NewFlagSet("evener", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := &runCLIFlags{launchFlags: registerLaunchFlags(fs)}

	flags.resumeWith = fs.String("resume-with", "", "start a new prompt using a previous session's `id` as context")
	flags.listSessions = fs.Bool("list-sessions", false, "list saved sessions and exit")
	flags.noDefaultMarketplaces = fs.Bool("no-default-marketplaces", false, "do not seed the default plugin marketplaces on first run")
	flags.runTimeout = fs.Duration("timeout", 0, "overall one-shot run timeout (0 disables; rate-limit retries use their finite fallback)")
	flags.askResponder = fs.String("ask-responder", "", "command run via `sh -c` to answer ask_user questions: gets the questions as JSON on stdin, its stdout is the reply; makes the session interactive (up to "+strconv.Itoa(askResponderMaxRounds)+" rounds per input)")
	fs.Usage = func() {
		printRunUsage(stderr, fs)
	}
	return fs, flags
}

func printRunUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(w, "Usage: evener --model <provider/model> [flags] <prompt>\n")
	_, _ = fmt.Fprintf(w, "       evener <command> [flags]\n\n")
	_, _ = fmt.Fprintf(w, "A non-interactive coding agent.\n\n")
	_, _ = fmt.Fprintf(w, "The prompt can be passed as arguments or piped via stdin.\n")
	_, _ = fmt.Fprintf(w, "--model can be omitted when %s supplies a default or when resuming.\n\n", envvars.EVENERModel.Name)
	_, _ = fmt.Fprintf(w, "Commands:\n")
	printRunCommands(w)
	_, _ = fmt.Fprintf(w, "\nRun 'evener <command> --help' for command-specific flags.\n\n")
	_, _ = fmt.Fprintf(w, "Options:\n")
	printLongFlagDefaults(w, fs)
	_, _ = fmt.Fprintf(w, "\nEnvironment variables:\n")
	printRunEnvVars(w)
}

func printStartupUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: evener <command> [flags]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Commands:")
	printRunCommands(w)
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Run 'evener hub' for the interactive web UI.")
	_, _ = fmt.Fprintln(w, "Run 'evener --help' for agent flags and environment variables.")
}

func printRunCommands(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  openai\tManage OpenAI OAuth login (login, logout, status)\n")
	_, _ = fmt.Fprintf(tw, "  serve\tRun the evener HTTP/RPC server\n")
	_, _ = fmt.Fprintf(tw, "  launch-check\tValidate launch contract for a provider/model\n")
	_, _ = fmt.Fprintf(tw, "  upgrade\tUpgrade installed Evener binaries\n")
	_, _ = fmt.Fprintf(tw, "  install\tInstall Evener on a remote macOS or Linux host over SSH\n")
	_, _ = fmt.Fprintf(tw, "  plugin\tManage plugin marketplaces and plugins\n")
	_, _ = fmt.Fprintf(tw, "  hub\tRun the interactive web UI\n")
	_, _ = fmt.Fprintf(tw, "  doctor\tRead-only forensic inspector for sessions/jobs/watches\n")
	_, _ = fmt.Fprintf(tw, "  migrate\tMigrate user data to the final evener layout\n")
	_, _ = fmt.Fprintf(tw, "  models\tInspect the provider registry (list, inspect, refresh)\n")
	_, _ = fmt.Fprintf(tw, "  providers\tInspect and author provider instances (list, probe, add)\n")
	_ = tw.Flush()
}

type boolFlag interface {
	IsBoolFlag() bool
}

func printLongFlagDefaults(w io.Writer, fs *flag.FlagSet) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fs.VisitAll(func(f *flag.Flag) {
		param, usage := flag.UnquoteUsage(f)
		option := "--" + f.Name
		if bf, ok := f.Value.(boolFlag); !ok || !bf.IsBoolFlag() {
			option += " <" + param + ">"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", option, usage)
	})
	_ = tw.Flush()
}

func printRunEnvVars(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range []envvars.Var{
		envvars.EVENERModel,
		envvars.EVENEROpenAIResponsesContinuation,
		envvars.EVENERReasoningEffort,
		envvars.EVENERStateDir,
		envvars.EVENERProvidersConfig,
		envvars.EVENERAllowedDecisions,
		envvars.OpenAIAPIKey,
		envvars.AnthropicAPIKey,
		envvars.GeminiAPIKey,
		envvars.GoogleAPIKey,
		envvars.OpenRouterAPIKey,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	// Every implicit provider the registry knows reads its own key and base
	// URL; naming them all here would be a second, drifting roster.
	_, _ = fmt.Fprintf(tw, "  %s\t%s\n", "<ID>_API_KEY / <ID>_BASE_URL", "any implicit provider's key or base URL (evener providers list)")
	_ = tw.Flush()
}

// subcommandExitError carries a non-zero exit code from a library-style
// subcommand (Run returning int) through the error-returning dispatch
// contract. It is not an error to print — the subcommand already wrote its
// own diagnostics.
type subcommandExitError int

func (c subcommandExitError) Error() string { return fmt.Sprintf("exit code %d", int(c)) }

// runWithScratchReclaim and runServeWithScratchReclaim are the production entry
// points to `evener` and `evener serve`. Reclaiming the session scratch that
// sessions retained and never came back for belongs to a real process start, so
// it hangs off these adapters rather than run and runServe themselves, where a
// test driving either would sweep the developer's own scratch bases. serve takes
// it as a dependency rather than up front because the workspace to leave alone
// is the one serve resolves from its own --dir.
func runWithScratchReclaim(ctx context.Context, cfg runConfig) error {
	startScratchReclaim(cfg.workDir)
	return run(ctx, cfg)
}

func runServeWithScratchReclaim(args []string) error {
	deps := defaultServeDeps()
	deps.reclaimScratch = startScratchReclaim
	return runServeWithDeps(args, deps)
}

func dispatchCLICommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, string, error) {
	return dispatchCLICommandWith(args, stdin, stdout, stderr, cliCommandRunners{
		serve: runServeWithScratchReclaim,
		launchCheck: func(args []string, _ io.Reader, stdout, stderr io.Writer) error {
			return launchcheck.RunLaunchCheck(args, stdout, stderr)
		},
		openAI: runOpenAI,
		upgrade: func(args []string, _ io.Reader, stdout, stderr io.Writer) error {
			return runUpgrade(args, stdout, stderr)
		},
		install: runInstall,
		plugin:  runPlugin,
		hub: func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			// The hub's attach subcommand bridges AppWire over this process's
			// stdin/stdout, so they must be threaded through rather than dropped:
			// nil makes it fall back to os.Stdin/os.Stdout, which breaks callers
			// that supply their own streams.
			if code := hubcmd.Run(args, stdin, stdout, stderr); code != 0 {
				return subcommandExitError(code)
			}
			return nil
		},
		doctor: func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			if code := doctorcmd.Run(args, stdin, stdout, stderr); code != 0 {
				return subcommandExitError(code)
			}
			return nil
		},
		migrate: func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			if code := migratecmd.Run(args, stdin, stdout, stderr); code != 0 {
				return subcommandExitError(code)
			}
			return nil
		},
		models:    runModels,
		providers: runProviders,
	})
}

type cliCommandRunners struct {
	serve       func([]string) error
	launchCheck func([]string, io.Reader, io.Writer, io.Writer) error
	openAI      func([]string, io.Reader, io.Writer, io.Writer) error
	upgrade     func([]string, io.Reader, io.Writer, io.Writer) error
	install     func([]string, io.Reader, io.Writer, io.Writer) error
	plugin      func([]string, io.Reader, io.Writer, io.Writer) error
	hub         func([]string, io.Reader, io.Writer, io.Writer) error
	doctor      func([]string, io.Reader, io.Writer, io.Writer) error
	migrate     func([]string, io.Reader, io.Writer, io.Writer) error
	models      func([]string, io.Reader, io.Writer, io.Writer) error
	providers   func([]string, io.Reader, io.Writer, io.Writer) error
}

func dispatchCLICommandWith(args []string, stdin io.Reader, stdout, stderr io.Writer, runners cliCommandRunners) (bool, string, error) {
	if len(args) == 0 {
		return false, "", nil
	}

	switch args[0] {
	case "serve":
		return true, "evener serve", runners.serve(args[1:])
	case "launch-check":
		return true, "evener launch-check", runners.launchCheck(args[1:], stdin, stdout, stderr)
	case "openai":
		return true, "evener openai", runners.openAI(args[1:], stdin, stdout, stderr)
	case "upgrade":
		return true, "evener upgrade", runners.upgrade(args[1:], stdin, stdout, stderr)
	case "install":
		return true, "evener install", runners.install(args[1:], stdin, stdout, stderr)
	case "plugin":
		return true, "evener plugin", runners.plugin(args[1:], stdin, stdout, stderr)
	case "hub":
		return true, "evener hub", runners.hub(args[1:], stdin, stdout, stderr)
	case "doctor":
		return true, "evener doctor", runners.doctor(args[1:], stdin, stdout, stderr)
	case "migrate":
		return true, "evener migrate", runners.migrate(args[1:], stdin, stdout, stderr)
	case "models":
		return true, "evener models", runners.models(args[1:], stdin, stdout, stderr)
	case "providers":
		return true, "evener providers", runners.providers(args[1:], stdin, stdout, stderr)
	default:
		return false, "", nil
	}
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"text/tabwriter"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf/internal/cliprompt"
	"primeradiant.com/serf/cmd/serf/internal/launchcheck"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	openaiprovider "primeradiant.com/serf/llm/providers/openai"
)

// Alias for brevity within flag definitions.
type stringSliceFlag = cmdutil.StringSliceFlag

type runCLIFlags struct {
	model              *string
	fastCheapModel     *string
	workDir            *string
	systemPrompt       *string
	stateDir           *string
	resume             *string
	resumeWith         *string
	resumeLast         *bool
	listSessions       *bool
	maxRounds          *int
	maxSubagentDepth   *int
	shareTaskStore     *bool
	resultToolName     *string
	reasoningEffort    *string
	exportATIF         *string
	contextStrategy    *string
	outputSchema       *string
	verbose            *bool
	noProjectPrompts   *bool
	agentName          *string
	skillsDirs         stringSliceFlag
	mcpServers         stringSliceFlag
	mcpConfigs         stringSliceFlag
	pluginDirs         stringSliceFlag
	systemPromptAsUser *bool
	cpuProfile         *string
	traceFile          *string
	systemPromptAppend stringSliceFlag
}

func main() {
	// Report the serf build version in the OpenAI provider's User-Agent and in
	// agent session metadata.
	openaiprovider.ClientVersion = buildinfo.Version()
	agent.BuildVersion = buildinfo.Version()

	// Quick flags that don't need full flag.Parse().
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("serf", buildinfo.VersionLong())
		return
	}

	// Subcommand dispatch — before flag.Parse() so subcommands get their own flag sets.
	if handled, label, err := dispatchCLICommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				// Subcommand printed usage via fs.Usage; exit cleanly.
				return
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
			os.Exit(1)
		}
		return
	}

	fs, flags := newRunFlagSet(os.Stderr)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}

	if *flags.cpuProfile != "" {
		stop, err := cmdutil.StartCPUProfile(*flags.cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf: %v\n", err)
			os.Exit(1)
		}
		defer stop()
	}
	if *flags.traceFile != "" {
		stop, err := cmdutil.StartTrace(*flags.traceFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf: %v\n", err)
			os.Exit(1) //nolint:gocritic // exitAfterDefer: fatal trace-setup failure; the optional cpu-profile finalizer is the only skipped defer
		}
		defer stop()
	}

	isResume := *flags.resume != "" || *flags.resumeWith != "" || *flags.resumeLast || *flags.listSessions
	stat, _ := os.Stdin.Stat()
	stdinIsCharDevice := stat != nil && (stat.Mode()&os.ModeCharDevice) != 0
	prompt := cliprompt.Read(fs.Args(), *flags.listSessions, os.Stdin, stdinIsCharDevice)

	if prompt == "" && !isResume {
		fs.Usage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err := run(ctx, runConfig{
		prompt:             prompt,
		model:              *flags.model,
		fastCheapModel:     *flags.fastCheapModel,
		workDir:            *flags.workDir,
		stateDir:           *flags.stateDir,
		systemPrompt:       *flags.systemPrompt,
		systemPromptAppend: []string(flags.systemPromptAppend),
		maxRounds:          *flags.maxRounds,
		maxSubagentDepth:   *flags.maxSubagentDepth,
		shareTaskStore:     *flags.shareTaskStore,
		resultToolName:     *flags.resultToolName,
		reasoningEffort:    *flags.reasoningEffort,
		contextStrategy:    *flags.contextStrategy,
		exportATIF:         *flags.exportATIF,
		outputSchema:       *flags.outputSchema,
		verbose:            *flags.verbose,
		noProjectPrompts:   *flags.noProjectPrompts,
		agentName:          *flags.agentName,
		skillsDirs:         []string(flags.skillsDirs),
		mcpServers:         []string(flags.mcpServers),
		mcpConfigs:         []string(flags.mcpConfigs),
		pluginDirs:         []string(flags.pluginDirs),
		systemPromptAsUser: *flags.systemPromptAsUser,
		stdout:             os.Stdout,
		stderr:             os.Stderr,
		resume:             *flags.resume,
		resumeWith:         *flags.resumeWith,
		resumeLast:         *flags.resumeLast,
		listSessions:       *flags.listSessions,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf: %v\n", err)
		cancel()
		os.Exit(1) //nolint:gocritic // cancel() called explicitly above
	}
}

func newRunFlagSet(stderr io.Writer) (*flag.FlagSet, *runCLIFlags) {
	fs := flag.NewFlagSet("serf", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := &runCLIFlags{}

	flags.model = fs.String("model", "", "LLM model identifier (`provider/model`)")
	flags.fastCheapModel = fs.String("fast-cheap-model", "", "auxiliary model for side calls (naming, summarization, web fetch); 'provider/model' may use a different provider than --model, or a bare 'model' for the active provider")
	flags.workDir = fs.String("dir", "", "working `directory` (default: current directory)")
	flags.systemPrompt = fs.String("system-prompt", "", "path to a custom system prompt `file`")
	flags.stateDir = fs.String("state-dir", "", "override runtime state `directory` (default: XDG-computed)")
	flags.resume = fs.String("resume", "", "resume a previous session by `id`")
	flags.resumeWith = fs.String("resume-with", "", "start a new prompt using a previous session's `id` as context")
	flags.resumeLast = fs.Bool("resume-last", false, "resume the most recent session")
	flags.listSessions = fs.Bool("list-sessions", false, "list saved sessions and exit")
	flags.maxRounds = fs.Int("max-rounds", -1, "max tool rounds per input (0=unlimited, default: 200)")
	flags.maxSubagentDepth = fs.Int("max-subagent-depth", -1, "max subagent nesting depth (default: 1)")
	flags.shareTaskStore = fs.Bool("share-task-store", false, "share task list between parent and child sessions")
	flags.resultToolName = fs.String("result-tool-name", "", "override the result tool `name` (default: communicate)")
	flags.reasoningEffort = fs.String("reasoning-effort", "", "reasoning effort `level`: minimal|low|medium|high|xhigh|max|none")
	flags.exportATIF = fs.String("export-atif", "", "export ATIF v1.7 trajectory to this `path` on session close")
	flags.contextStrategy = fs.String("context-strategy", "", "context management `strategy`: compact|session-log|ooda (default: compact)")
	flags.outputSchema = fs.String("output-schema", "", "inline JSON Schema `document` applied to the communicate tool's output field (replaces the default schema)")
	flags.verbose = fs.Bool("verbose", false, "emit NDJSON events to stderr")
	flags.noProjectPrompts = fs.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading (match container behavior)")
	flags.agentName = fs.String("agent", "", "agent persona `name`: default (default), explorer, or another available agent name")
	fs.Var(&flags.skillsDirs, "skills-dir", "extra skill `directory` (repeatable)")
	fs.Var(&flags.mcpServers, "mcp", "MCP server `spec` (repeatable, format: name:command args...)")
	fs.Var(&flags.mcpConfigs, "mcp-config", "path to .mcp.json `file` (repeatable)")
	fs.Var(&flags.pluginDirs, "plugin-dir", "plugin `directory` (repeatable)")
	flags.systemPromptAsUser = fs.Bool("system-prompt-as-user", false, "deliver system prompt as first user message instead of system instructions")
	flags.cpuProfile = fs.String("cpu-profile", "", "write CPU profile to this `file` path")
	flags.traceFile = fs.String("trace", "", "write execution trace to this `file` path")
	fs.Var(&flags.systemPromptAppend, "system-prompt-append", "path to append to system prompt `file` (repeatable)")

	fs.Usage = func() {
		printRunUsage(stderr, fs)
	}
	return fs, flags
}

func printRunUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(w, "Usage: serf --model <provider/model> [flags] <prompt>\n")
	_, _ = fmt.Fprintf(w, "       serf <command> [flags]\n\n")
	_, _ = fmt.Fprintf(w, "A non-interactive coding agent.\n\n")
	_, _ = fmt.Fprintf(w, "The prompt can be passed as arguments or piped via stdin.\n")
	_, _ = fmt.Fprintf(w, "--model can be omitted when %s supplies a default or when resuming.\n\n", envvars.SERFModel.Name)
	_, _ = fmt.Fprintf(w, "Commands:\n")
	printRunCommands(w)
	_, _ = fmt.Fprintf(w, "\nRun 'serf <command> --help' for command-specific flags.\n\n")
	_, _ = fmt.Fprintf(w, "Options:\n")
	printLongFlagDefaults(w, fs)
	_, _ = fmt.Fprintf(w, "\nEnvironment variables:\n")
	printRunEnvVars(w)
}

func printRunCommands(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  openai\tManage OpenAI OAuth login (login, logout, status)\n")
	_, _ = fmt.Fprintf(tw, "  serve\tRun the serf HTTP/RPC server\n")
	_, _ = fmt.Fprintf(tw, "  launch-check\tValidate launch contract for a provider/model\n")
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
		envvars.SERFModel,
		envvars.SERFReasoningEffort,
		envvars.SERFStateDir,
		envvars.SERFProvidersConfig,
		envvars.SERFAllowedDecisions,
		envvars.OpenAIAPIKey,
		envvars.AnthropicAPIKey,
		envvars.GeminiAPIKey,
		envvars.GoogleAPIKey,
		envvars.OpenRouterAPIKey,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	_ = tw.Flush()
}

func dispatchCLICommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, string, error) {
	if len(args) == 0 {
		return false, "", nil
	}

	switch args[0] {
	case "serve":
		return true, "serf serve", runServe(args[1:])
	case "launch-check":
		return true, "serf launch-check", launchcheck.RunLaunchCheck(args[1:], stdout, stderr)
	case "openai":
		return true, "serf openai", runOpenAI(args[1:], stdin, stdout, stderr)
	default:
		return false, "", nil
	}
}

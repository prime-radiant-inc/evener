package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmdutil"
)

// Alias for brevity within flag definitions.
type stringSliceFlag = cmdutil.StringSliceFlag

func main() {
	// Quick flags that don't need full flag.Parse().
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("serf", buildinfo.VersionLong())
		return
	}

	// Subcommand dispatch — before flag.Parse() so serve gets its own flag set.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "serf serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	model := flag.String("model", "", "LLM model identifier")
	provider := flag.String("provider", "", "LLM provider (openai, anthropic, google)")
	workDir := flag.String("dir", "", "working directory (default: current directory)")
	systemPrompt := flag.String("system-prompt", "", "path to a custom system prompt file")
	stateDir := flag.String("state-dir", "", "override runtime state directory (default: XDG-computed)")
	resume := flag.String("resume", "", "resume a previous session by ID")
	resumeWith := flag.String("resume-with", "", "start a new task using a previous session's context")
	resumeLast := flag.Bool("resume-last", false, "resume the most recent session")
	listSessionsFlag := flag.Bool("list-sessions", false, "list saved sessions and exit")
	maxRounds := flag.Int("max-rounds", -1, "max tool rounds per input (0=unlimited, default: 200)")
	minResultRound := flag.Int("min-result-round", 0, "minimum round before communicate is accepted (0=no minimum)")
	enableReviewerGate := flag.Bool("enable-reviewer-gate", false, "spawn reviewer subagent to validate communicate at depth 0")
	resultToolName := flag.String("result-tool-name", "", "override the result tool name (default: communicate)")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning effort: low|medium|high|xhigh|none")
	contextStrategy := flag.String("context-strategy", "", "context management strategy: compact|recall|session-log|ooda (default: compact)")
	verbose := flag.Bool("verbose", false, "emit NDJSON events to stderr")
	noProjectPrompts := flag.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading (match container behavior)")
	var skillsDirs stringSliceFlag
	flag.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers stringSliceFlag
	flag.Var(&mcpServers, "mcp", "MCP server (repeatable, format: name:command args...)")
	var mcpConfigs stringSliceFlag
	flag.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
	var pluginDirs stringSliceFlag
	flag.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")
	var systemPromptAppend stringSliceFlag
	flag.Var(&systemPromptAppend, "system-prompt-append", "path to append to system prompt (repeatable)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf --provider <provider> --model <model> [flags] <task>\n\n")
		fmt.Fprintf(os.Stderr, "A non-interactive coding agent.\n\n")
		fmt.Fprintf(os.Stderr, "The task can be passed as arguments or piped via stdin.\n\n")
		fmt.Fprintf(os.Stderr, "Required:\n")
		fmt.Fprintf(os.Stderr, "  --provider <name>    LLM provider: openai, anthropic, google\n")
		fmt.Fprintf(os.Stderr, "  --model <name>       LLM model (e.g. gpt-5.2, claude-opus-4-6, gemini-3-flash-preview)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  --dir <path>         Working directory (default: current directory)\n")
		fmt.Fprintf(os.Stderr, "  --system-prompt <path> Path to a custom system prompt file (replaces default)\n")
		fmt.Fprintf(os.Stderr, "  --system-prompt-append <path> Append to system prompt (repeatable)\n")
		fmt.Fprintf(os.Stderr, "  --state-dir <path>   Override runtime state directory (sessions, tasks)\n")
		fmt.Fprintf(os.Stderr, "  --max-rounds <n>     Max tool rounds per input (0=unlimited, default: 200)\n")
		fmt.Fprintf(os.Stderr, "  --min-result-round <n> Minimum round before result submission accepted (default: 0=no minimum)\n")
		fmt.Fprintf(os.Stderr, "  --context-strategy <name> Context management strategy: compact|recall|session-log|ooda (default: compact)\n")
		fmt.Fprintf(os.Stderr, "  --verbose            Emit NDJSON events to stderr (replaces human-readable output)\n")
		fmt.Fprintf(os.Stderr, "  --no-project-prompts Suppress .serf/prompts/ loading (match Docker container behavior)\n")
		fmt.Fprintf(os.Stderr, "  --skills-dir <path>  Extra skill directory (repeatable)\n")
		fmt.Fprintf(os.Stderr, "  --mcp <spec>         MCP server (repeatable, format: name:command args...)\n")
		fmt.Fprintf(os.Stderr, "  --mcp-config <path>  Path to .mcp.json file (repeatable)\n")
		fmt.Fprintf(os.Stderr, "  --plugin-dir <path>  Plugin directory (repeatable)\n\n")
		fmt.Fprintf(os.Stderr, "Session resume:\n")
		fmt.Fprintf(os.Stderr, "  --resume <id>        Resume a previous session\n")
		fmt.Fprintf(os.Stderr, "  --resume-with <id>   New task using a previous session's context\n")
		fmt.Fprintf(os.Stderr, "  --resume-last        Resume the most recent session\n")
		fmt.Fprintf(os.Stderr, "  --list-sessions      List saved sessions\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables:\n")
		fmt.Fprintf(os.Stderr, "  SERF_MODEL           Default model (used when --model is omitted)\n")
		fmt.Fprintf(os.Stderr, "  SERF_PROVIDER        Default provider (used when --provider is omitted)\n")
		fmt.Fprintf(os.Stderr, "  SERF_REASONING_EFFORT Default reasoning effort (low|medium|high|xhigh|none)\n")
		fmt.Fprintf(os.Stderr, "  OPENAI_API_KEY       OpenAI API key\n")
		fmt.Fprintf(os.Stderr, "  ANTHROPIC_API_KEY    Anthropic API key\n")
		fmt.Fprintf(os.Stderr, "  GEMINI_API_KEY       Google Gemini API key\n")
	}
	flag.Parse()

	isResume := *resume != "" || *resumeWith != "" || *resumeLast || *listSessionsFlag
	stat, _ := os.Stdin.Stat()
	stdinIsCharDevice := stat != nil && (stat.Mode()&os.ModeCharDevice) != 0
	task := readTaskFromArgsOrStdin(flag.Args(), *listSessionsFlag, os.Stdin, stdinIsCharDevice)

	if task == "" && !isResume {
		flag.Usage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err := run(ctx, runConfig{
		task:               task,
		model:              *model,
		provider:           *provider,
		workDir:            *workDir,
		stateDir:           *stateDir,
		systemPrompt:       *systemPrompt,
		systemPromptAppend: []string(systemPromptAppend),
		maxRounds:          *maxRounds,
		minResultRound:     *minResultRound,
		enableReviewerGate: *enableReviewerGate,
		resultToolName:     *resultToolName,
		reasoningEffort:    *reasoningEffort,
		contextStrategy:    *contextStrategy,
		verbose:            *verbose,
		noProjectPrompts:   *noProjectPrompts,
		skillsDirs:         []string(skillsDirs),
		mcpServers:         []string(mcpServers),
		mcpConfigs:         []string(mcpConfigs),
		pluginDirs:         []string(pluginDirs),
		stdout:             os.Stdout,
		stderr:             os.Stderr,
		resume:             *resume,
		resumeWith:         *resumeWith,
		resumeLast:         *resumeLast,
		listSessions:       *listSessionsFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf: %v\n", err)
		cancel()
		os.Exit(1) //nolint:gocritic // cancel() called explicitly above
	}
}

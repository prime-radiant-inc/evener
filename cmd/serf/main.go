package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

// stringSliceFlag implements flag.Value for a repeatable string flag.
type stringSliceFlag []string

func (f *stringSliceFlag) String() string { return strings.Join(*f, ",") }
func (f *stringSliceFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

func main() {
	model := flag.String("model", "", "LLM model identifier")
	provider := flag.String("provider", "", "LLM provider (openai, anthropic, google)")
	workDir := flag.String("dir", "", "working directory (default: current directory)")
	systemPrompt := flag.String("system-prompt", "", "path to a custom system prompt file")
	stateDir := flag.String("state-dir", "", "override runtime state directory (default: XDG-computed)")
	resume := flag.String("resume", "", "resume a previous session by ID")
	resumeWith := flag.String("resume-with", "", "start a new task using a previous session's context")
	resumeLast := flag.Bool("resume-last", false, "resume the most recent session")
	listSessionsFlag := flag.Bool("list-sessions", false, "list saved sessions and exit")
	verbose := flag.Bool("verbose", false, "emit NDJSON events to stderr")
	var skillsDirs stringSliceFlag
	flag.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers stringSliceFlag
	flag.Var(&mcpServers, "mcp", "MCP server (repeatable, format: name:command args...)")
	var mcpConfigs stringSliceFlag
	flag.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
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
		fmt.Fprintf(os.Stderr, "  --verbose            Emit NDJSON events to stderr (replaces human-readable output)\n")
		fmt.Fprintf(os.Stderr, "  --skills-dir <path>  Extra skill directory (repeatable)\n")
		fmt.Fprintf(os.Stderr, "  --mcp <spec>         MCP server (repeatable, format: name:command args...)\n")
		fmt.Fprintf(os.Stderr, "  --mcp-config <path>  Path to .mcp.json file (repeatable)\n\n")
		fmt.Fprintf(os.Stderr, "Session resume:\n")
		fmt.Fprintf(os.Stderr, "  --resume <id>        Resume a previous session\n")
		fmt.Fprintf(os.Stderr, "  --resume-with <id>   New task using a previous session's context\n")
		fmt.Fprintf(os.Stderr, "  --resume-last        Resume the most recent session\n")
		fmt.Fprintf(os.Stderr, "  --list-sessions      List saved sessions\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables:\n")
		fmt.Fprintf(os.Stderr, "  SERF_MODEL           Default model (used when --model is omitted)\n")
		fmt.Fprintf(os.Stderr, "  SERF_PROVIDER        Default provider (used when --provider is omitted)\n")
		fmt.Fprintf(os.Stderr, "  OPENAI_API_KEY       OpenAI API key\n")
		fmt.Fprintf(os.Stderr, "  ANTHROPIC_API_KEY    Anthropic API key\n")
		fmt.Fprintf(os.Stderr, "  GEMINI_API_KEY       Google Gemini API key\n")
	}
	flag.Parse()

	task := strings.TrimSpace(strings.Join(flag.Args(), " "))

	// If no args, try reading from stdin (piped input).
	isResume := *resume != "" || *resumeWith != "" || *resumeLast || *listSessionsFlag
	if task == "" && !isResume {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			b, err := io.ReadAll(os.Stdin)
			if err == nil {
				task = strings.TrimSpace(string(b))
			}
		}
	}

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
		verbose:            *verbose,
		skillsDirs:         []string(skillsDirs),
		mcpServers:         []string(mcpServers),
		mcpConfigs:         []string(mcpConfigs),
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

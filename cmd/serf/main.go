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

func main() {
	model := flag.String("model", "", "LLM model identifier (e.g., gpt-5-mini-2025-08-07, claude-opus-4-6)")
	workDir := flag.String("dir", "", "working directory (default: current directory)")
	resume := flag.String("resume", "", "resume a previous session by ID")
	resumeWith := flag.String("resume-with", "", "start a new task using a previous session's context")
	resumeLast := flag.Bool("resume-last", false, "resume the most recent session")
	listSessionsFlag := flag.Bool("list-sessions", false, "list saved sessions and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf [flags] <task>\n\n")
		fmt.Fprintf(os.Stderr, "A non-interactive coding agent.\n\n")
		fmt.Fprintf(os.Stderr, "The task can be passed as arguments or piped via stdin.\n\n")
		fmt.Fprintf(os.Stderr, "Session resume:\n")
		fmt.Fprintf(os.Stderr, "  --resume <id>        Resume a previous session\n")
		fmt.Fprintf(os.Stderr, "  --resume-with <id>   New task using a previous session's context\n")
		fmt.Fprintf(os.Stderr, "  --resume-last        Resume the most recent session\n")
		fmt.Fprintf(os.Stderr, "  --list-sessions      List saved sessions\n\n")
		fmt.Fprintf(os.Stderr, "Environment variables:\n")
		fmt.Fprintf(os.Stderr, "  OPENAI_API_KEY       OpenAI API key\n")
		fmt.Fprintf(os.Stderr, "  ANTHROPIC_API_KEY    Anthropic API key\n")
		fmt.Fprintf(os.Stderr, "  GEMINI_API_KEY       Google Gemini API key\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
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
		task:         task,
		model:        *model,
		workDir:      *workDir,
		stdout:       os.Stdout,
		stderr:       os.Stderr,
		resume:       *resume,
		resumeWith:   *resumeWith,
		resumeLast:   *resumeLast,
		listSessions: *listSessionsFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf: %v\n", err)
		os.Exit(1)
	}
}

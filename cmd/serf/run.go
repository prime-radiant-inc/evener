package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/prime-radiant/serf/internal/agent"
	"github.com/prime-radiant/serf/internal/llm"
	_ "github.com/prime-radiant/serf/internal/llm/providers/anthropic"
	_ "github.com/prime-radiant/serf/internal/llm/providers/google"
	_ "github.com/prime-radiant/serf/internal/llm/providers/openai"
)

type runConfig struct {
	task    string
	model   string
	workDir string
	stdout  io.Writer
	stderr  io.Writer

	// Resume options.
	resume       string // session ID to resume
	resumeWith   string // session ID whose context to reuse with a new task
	resumeLast   bool   // resume the most recent session
	listSessions bool   // print saved sessions and exit
}

func run(ctx context.Context, cfg runConfig) error {
	if cfg.stdout == nil {
		cfg.stdout = os.Stdout
	}
	if cfg.stderr == nil {
		cfg.stderr = os.Stderr
	}
	if cfg.workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
		cfg.workDir = wd
	}

	// --list-sessions: print and exit.
	if cfg.listSessions {
		return listSessions(cfg)
	}

	// Resolve resume target.
	var snap *agent.SessionSnapshot
	if cfg.resume != "" || cfg.resumeWith != "" || cfg.resumeLast {
		s, err := resolveSnapshot(cfg)
		if err != nil {
			return err
		}
		snap = &s
	}

	// Determine task.
	task := strings.TrimSpace(cfg.task)
	if snap != nil && cfg.resumeWith == "" && task == "" {
		// --resume without a task: continue with a generic prompt.
		task = "Continue where you left off."
	}
	if task == "" && snap == nil {
		return fmt.Errorf("no task provided")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	model := cfg.model
	if model == "" && snap != nil {
		model = snap.Model
	}
	profile := selectProfile(client, model)
	env := agent.NewLocalExecutionEnvironment(cfg.workDir)

	var sess *agent.Session
	if snap != nil {
		sess, err = agent.RestoreSession(client, profile, env, *snap)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", snap.ID, snap.TurnCount)
	} else {
		sess, err = agent.NewSession(client, profile, env, agent.SessionConfig{
			MaxToolRoundsPerInput: 200,
			AutoSaveDir:           cfg.workDir,
		})
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}
	defer sess.Close()

	done := drainEvents(sess, cfg.stderr)

	result, err := sess.ProcessInput(ctx, task)
	sess.Close()
	<-done

	if err != nil {
		return err
	}

	fmt.Fprintln(cfg.stdout, result)
	return nil
}

// drainEvents starts a goroutine that reads session events and prints tool
// activity to w. Returns a channel that closes when draining completes.
func drainEvents(sess *agent.Session, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			switch ev.Kind {
			case agent.EventToolCallStart:
				name, _ := ev.Data["tool_name"].(string)
				fmt.Fprintf(w, "[tool] %s\n", name)
			case agent.EventToolCallEnd:
				name, _ := ev.Data["tool_name"].(string)
				isErr, _ := ev.Data["is_error"].(bool)
				if isErr {
					fmt.Fprintf(w, "[tool] %s: error\n", name)
				} else {
					fmt.Fprintf(w, "[tool] %s: done\n", name)
				}
			case agent.EventError:
				errMsg, _ := ev.Data["error"].(string)
				fmt.Fprintf(w, "[error] %s\n", errMsg)
			}
		}
	}()
	return done
}

// resolveSnapshot loads the session snapshot for the given resume configuration.
func resolveSnapshot(cfg runConfig) (agent.SessionSnapshot, error) {
	if cfg.resumeLast {
		list, err := agent.ListSessions(cfg.workDir)
		if err != nil {
			return agent.SessionSnapshot{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return agent.SessionSnapshot{}, fmt.Errorf("no saved sessions in %s", cfg.workDir)
		}
		return list[0], nil
	}

	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	snap, err := agent.LoadSession(cfg.workDir, id)
	if err != nil {
		return agent.SessionSnapshot{}, fmt.Errorf("load session %s: %w", id, err)
	}
	return snap, nil
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig) error {
	list, err := agent.ListSessions(cfg.workDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(cfg.stdout, "No saved sessions.")
		return nil
	}
	for _, s := range list {
		firstInput := ""
		for _, t := range s.History {
			if t.Kind == agent.TurnUserInput {
				firstInput = t.Message.Text()
				break
			}
		}
		if len(firstInput) > 80 {
			firstInput = firstInput[:77] + "..."
		}
		fmt.Fprintf(cfg.stdout, "%s  %-16s  %-20s  turns=%d  %q\n",
			s.ID, s.Model, s.UpdatedAt.Format("2006-01-02 15:04:05"), s.TurnCount, firstInput)
	}
	return nil
}

// selectProfile picks the right ProviderProfile based on available providers and model.
func selectProfile(client *llm.Client, model string) agent.ProviderProfile {
	providers := client.ProviderNames()

	// If model is specified, try to infer provider from model name.
	if model != "" {
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "gpt") || strings.Contains(lower, "codex") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3") || strings.Contains(lower, "o4"):
			return agent.NewOpenAIProfile(model)
		case strings.Contains(lower, "claude") || strings.Contains(lower, "sonnet") || strings.Contains(lower, "opus") || strings.Contains(lower, "haiku"):
			return agent.NewAnthropicProfile(model)
		case strings.Contains(lower, "gemini"):
			return agent.NewGeminiProfile(model)
		}
	}

	// Fall back to first available provider.
	for _, p := range providers {
		switch p {
		case "openai":
			m := model
			if m == "" {
				m = "gpt-5.2"
			}
			return agent.NewOpenAIProfile(m)
		case "anthropic":
			m := model
			if m == "" {
				m = "claude-opus-4-6"
			}
			return agent.NewAnthropicProfile(m)
		case "google":
			m := model
			if m == "" {
				m = "gemini-3-flash-preview"
			}
			return agent.NewGeminiProfile(m)
		}
	}

	// Should not happen if NewFromEnv succeeded, but fallback.
	m := model
	if m == "" {
		m = "gpt-5.2"
	}
	return agent.NewOpenAIProfile(m)
}

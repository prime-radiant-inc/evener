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
}

func run(ctx context.Context, cfg runConfig) error {
	if strings.TrimSpace(cfg.task) == "" {
		return fmt.Errorf("no task provided")
	}

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

	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	profile := selectProfile(client, cfg.model)
	env := agent.NewLocalExecutionEnvironment(cfg.workDir)

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		return fmt.Errorf("session creation: %w", err)
	}
	defer sess.Close()

	// Drain events in background and print tool activity to stderr.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			switch ev.Kind {
			case agent.EventToolCallStart:
				name, _ := ev.Data["tool_name"].(string)
				fmt.Fprintf(cfg.stderr, "[tool] %s\n", name)
			case agent.EventToolCallEnd:
				name, _ := ev.Data["tool_name"].(string)
				isErr, _ := ev.Data["is_error"].(bool)
				if isErr {
					fmt.Fprintf(cfg.stderr, "[tool] %s: error\n", name)
				} else {
					fmt.Fprintf(cfg.stderr, "[tool] %s: done\n", name)
				}
			case agent.EventError:
				errMsg, _ := ev.Data["error"].(string)
				fmt.Fprintf(cfg.stderr, "[error] %s\n", errMsg)
			}
		}
	}()

	result, err := sess.ProcessInput(ctx, cfg.task)
	sess.Close()
	<-done

	if err != nil {
		return err
	}

	fmt.Fprintln(cfg.stdout, result)
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

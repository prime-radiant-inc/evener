package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"primeradiant.com/serf/internal/agent"
	"primeradiant.com/serf/internal/llm"
	_ "primeradiant.com/serf/internal/llm/providers/anthropic"
	_ "primeradiant.com/serf/internal/llm/providers/google"
	_ "primeradiant.com/serf/internal/llm/providers/openai"
)

type runConfig struct {
	task         string
	model        string
	provider     string
	workDir      string
	stateDir     string // --state-dir override
	systemPrompt       string   // --system-prompt file path
	systemPromptAppend []string // --system-prompt-append file paths
	verbose            bool
	stdout       io.Writer
	stderr       io.Writer

	skillsDirs []string // extra skill directories
	mcpServers []string // --mcp inline specs
	mcpConfigs []string // --mcp-config file paths

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

	// Compute runtime state directory.
	stateDir := cfg.stateDir
	if stateDir == "" {
		originURL := gitOriginURLFromDir(cfg.workDir)
		stateDir = agent.RuntimeDir(originURL, cfg.workDir, "")
	}

	// --list-sessions: print and exit.
	if cfg.listSessions {
		return listSessions(cfg, stateDir)
	}

	// Resolve resume target.
	var snap *agent.SessionSnapshot
	if cfg.resume != "" || cfg.resumeWith != "" || cfg.resumeLast {
		s, err := resolveSnapshot(cfg, stateDir)
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

	// Resolve provider: explicit flag > snapshot > SERF_PROVIDER env var.
	provider := cfg.provider
	if provider == "" && snap != nil {
		provider = snap.ProfileID
	}
	if provider == "" {
		provider = os.Getenv("SERF_PROVIDER")
	}
	if provider == "" {
		return fmt.Errorf("no provider specified: use --provider or set SERF_PROVIDER (openai, anthropic, google)")
	}

	// Resolve model: explicit flag > snapshot > SERF_MODEL env var.
	model := cfg.model
	if model == "" && snap != nil {
		model = snap.Model
	}
	if model == "" {
		model = os.Getenv("SERF_MODEL")
	}
	if model == "" {
		return fmt.Errorf("no model specified: use --model or set SERF_MODEL")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	profile, err := selectProfile(provider, model)
	if err != nil {
		return err
	}
	env := agent.NewLocalExecutionEnvironment(cfg.workDir)

	var sess *agent.Session
	if snap != nil {
		sess, err = agent.RestoreSession(client, profile, env, *snap, stateDir)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", snap.ID, snap.TurnCount)
	} else {
		sess, err = agent.NewSession(client, profile, env, agent.SessionConfig{
			MaxToolRoundsPerInput: 200,
			StateDir:              stateDir,
			SystemPromptFile:      cfg.systemPrompt,
			SystemPromptAppend:    cfg.systemPromptAppend,
			SkillsDirs:            cfg.skillsDirs,
			MCPConfigFiles:        cfg.mcpConfigs,
			MCPInline:             cfg.mcpServers,
		})
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}
	defer sess.Close()

	var done <-chan struct{}
	if cfg.verbose {
		done = drainEventsVerbose(sess.Events(), cfg.stderr)
	} else {
		done = drainEventsHuman(sess.Events(), cfg.stderr)
	}

	result, err := sess.ProcessInput(ctx, task)
	sess.Close()
	<-done

	if err != nil {
		return err
	}

	fmt.Fprintln(cfg.stdout, result)
	return nil
}

// drainEventsVerbose writes every event as a JSON line (NDJSON) to w.
func drainEventsVerbose(events <-chan agent.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for ev := range events {
			enc.Encode(ev) //nolint:errcheck
		}
	}()
	return done
}

// drainEventsHuman writes human-readable status lines to w.
func drainEventsHuman(events <-chan agent.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			switch ev.Kind {
			case agent.EventSessionStart:
				model, _ := ev.Data["model"].(string)
				profile, _ := ev.Data["profile"].(string)
				if model != "" {
					fmt.Fprintf(w, "[model] %s (%s)\n", model, profile)
				}
			case agent.EventAssistantTextEnd:
				txt, _ := ev.Data["text"].(string)
				if strings.TrimSpace(txt) != "" {
					fmt.Fprintf(w, "[assistant] %s\n", txt)
				}
				if reasoning, _ := ev.Data["reasoning"].(string); reasoning != "" {
					fmt.Fprintf(w, "[thinking] (%d chars)\n", len(reasoning))
				}
				if usage, ok := ev.Data["usage"].(llm.Usage); ok {
					line := fmt.Sprintf("[usage] in=%d out=%d total=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
					if usage.CacheReadTokens != nil {
						line += fmt.Sprintf(" cache_read=%d", *usage.CacheReadTokens)
					}
					if usage.CacheWriteTokens != nil {
						line += fmt.Sprintf(" cache_write=%d", *usage.CacheWriteTokens)
					}
					fmt.Fprintln(w, line)
				}
			case agent.EventToolCallStart:
				name, _ := ev.Data["tool_name"].(string)
				args, _ := ev.Data["arguments_json"].(string)
				if len(args) > 100 {
					args = args[:97] + "..."
				}
				fmt.Fprintf(w, "[tool] %s %s\n", name, args)
			case agent.EventToolCallEnd:
				name, _ := ev.Data["tool_name"].(string)
				isErr, _ := ev.Data["is_error"].(bool)
				if isErr {
					fmt.Fprintf(w, "[tool] %s: error\n", name)
				} else {
					fmt.Fprintf(w, "[tool] %s: done\n", name)
				}
			case agent.EventCommunicate:
				action, _ := ev.Data["action"].(string)
				msg, _ := ev.Data["message"].(string)
				if action == "status" {
					fmt.Fprintf(w, "[status] %s\n", msg)
				}
				// result is printed via ProcessInput return value on stdout
			case agent.EventSkillActivated:
				name, _ := ev.Data["name"].(string)
				fmt.Fprintf(w, "[skill] activated %s\n", name)
			case agent.EventWarning:
				msg, _ := ev.Data["message"].(string)
				fmt.Fprintf(w, "[warning] %s\n", msg)
			case agent.EventError:
				errMsg, _ := ev.Data["error"].(string)
				fmt.Fprintf(w, "[error] %s\n", errMsg)
			}
		}
	}()
	return done
}

// resolveSnapshot loads the session snapshot for the given resume configuration.
func resolveSnapshot(cfg runConfig, stateDir string) (agent.SessionSnapshot, error) {
	if cfg.resumeLast {
		list, err := agent.ListSessions(stateDir)
		if err != nil {
			return agent.SessionSnapshot{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return agent.SessionSnapshot{}, fmt.Errorf("no saved sessions in %s", stateDir)
		}
		return list[0], nil
	}

	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	snap, err := agent.LoadSession(stateDir, id)
	if err != nil {
		return agent.SessionSnapshot{}, fmt.Errorf("load session %s: %w", id, err)
	}
	return snap, nil
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig, stateDir string) error {
	list, err := agent.ListSessions(stateDir)
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
		branch := s.EnvInfo.GitBranch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(cfg.stdout, "%s  %-16s  %-20s  %-20s  turns=%d  %q\n",
			s.ID, s.Model, branch, s.UpdatedAt.Format("2006-01-02 15:04:05"), s.TurnCount, firstInput)
	}
	return nil
}

// gitOriginURLFromDir runs "git remote get-url origin" in dir and returns the
// URL, or "" if not a git repo or no origin remote.
func gitOriginURLFromDir(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// selectProfile creates the ProviderProfile for the given provider and model.
func selectProfile(provider, model string) (agent.ProviderProfile, error) {
	switch strings.ToLower(provider) {
	case "openai":
		return agent.NewOpenAIProfile(model), nil
	case "anthropic":
		return agent.NewAnthropicProfile(model), nil
	case "google", "gemini":
		return agent.NewGeminiProfile(model), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: must be openai, anthropic, or google", provider)
	}
}

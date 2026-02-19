package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
)

type runConfig struct {
	task               string
	model              string
	provider           string
	workDir            string
	stateDir           string   // --state-dir override
	systemPrompt       string   // --system-prompt file path
	systemPromptAppend []string // --system-prompt-append file paths
	maxRounds          int      // --max-rounds (-1=default, 0=unlimited, >0=limit)
	reasoningEffort    string   // --reasoning-effort override (or SERF_REASONING_EFFORT)
	contextStrategy    string   // --context-strategy
	verbose            bool
	stdout             io.Writer
	stderr             io.Writer

	skillsDirs []string // extra skill directories
	mcpServers []string // --mcp inline specs
	mcpConfigs []string // --mcp-config file paths
	pluginDirs []string // --plugin-dir directories

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
		originURL := cmdutil.GitOriginURLFromDir(cfg.workDir)
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

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
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

	profile, err := cmdutil.SelectProfile(provider, model)
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
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", snap.ID, snap.TurnCount) //nolint:errcheck
	} else {
		sessionCfg := agent.SessionConfig{
			MaxToolRoundsPerInput: cmdutil.MaxRoundsToConfig(cfg.maxRounds),
			StateDir:              stateDir,
			SystemPromptFile:      cfg.systemPrompt,
			SystemPromptAppend:    cfg.systemPromptAppend,
			SkillsDirs:            cfg.skillsDirs,
			MCPConfigFiles:        cfg.mcpConfigs,
			MCPInline:             cfg.mcpServers,
			PluginDirs:            cfg.pluginDirs,
			ContextStrategy:       cfg.contextStrategy,
		}
		if effort.Set {
			sessionCfg.ReasoningEffort = effort.Value
		}
		sess, err = agent.NewSession(client, profile, env, sessionCfg)
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

	fmt.Fprintln(cfg.stdout, result) //nolint:errcheck
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
				if d, ok := ev.Data.(agent.SessionStartData); ok && d.Model != "" {
					fmt.Fprintf(w, "[model] %s (%s)\n", d.Model, d.Profile) //nolint:errcheck
				}
			case agent.EventAssistantTextEnd:
				if d, ok := ev.Data.(agent.AssistantTextEndData); ok {
					if strings.TrimSpace(d.Text) != "" {
						fmt.Fprintf(w, "[assistant] %s\n", d.Text) //nolint:errcheck
					}
					if d.Reasoning != "" {
						fmt.Fprintf(w, "[thinking] (%d chars)\n", len(d.Reasoning)) //nolint:errcheck
					}
					if usage, ok := d.Usage.(llm.Usage); ok {
						line := fmt.Sprintf("[usage] in=%d out=%d total=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
						if usage.CacheReadTokens != nil {
							line += fmt.Sprintf(" cache_read=%d", *usage.CacheReadTokens)
						}
						if usage.CacheWriteTokens != nil {
							line += fmt.Sprintf(" cache_write=%d", *usage.CacheWriteTokens)
						}
						fmt.Fprintln(w, line) //nolint:errcheck
					}
				}
			case agent.EventToolCallStart:
				if d, ok := ev.Data.(agent.ToolCallStartData); ok {
					args := d.ArgumentsJSON
					if len(args) > 100 {
						args = args[:97] + "..."
					}
					fmt.Fprintf(w, "[tool] %s %s\n", d.ToolName, args) //nolint:errcheck
				}
			case agent.EventToolCallEnd:
				if d, ok := ev.Data.(agent.ToolCallEndData); ok {
					if d.Error != "" {
						fmt.Fprintf(w, "[tool] %s: error\n", d.ToolName) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[tool] %s: done\n", d.ToolName) //nolint:errcheck
					}
				}
			case agent.EventCommunicate:
				if d, ok := ev.Data.(agent.CommunicateData); ok && d.Action == "status" {
					fmt.Fprintf(w, "[status] %s\n", d.Message) //nolint:errcheck
				}
				// result is printed via ProcessInput return value on stdout
				case agent.EventPluginLoaded:
				if d, ok := ev.Data.(agent.PluginLoadedData); ok {
					fmt.Fprintf(w, "[plugin] loaded %s (%d skills, %d agents, %d mcp)\n", //nolint:errcheck
						d.Name, d.SkillCount, d.AgentCount, d.MCPCount)
				}
			case agent.EventHookStart:
				if d, ok := ev.Data.(agent.HookStartData); ok {
					fmt.Fprintf(w, "[hook] %s %s (%s)\n", d.Event, d.Matcher, d.HookType) //nolint:errcheck
				}
			case agent.EventHookEnd:
				if d, ok := ev.Data.(agent.HookEndData); ok {
					fmt.Fprintf(w, "[hook] %s %s done (%dms)\n", d.Event, d.Matcher, d.DurationMS) //nolint:errcheck
				}
			case agent.EventSkillActivated:
				if d, ok := ev.Data.(agent.SkillActivatedData); ok {
					fmt.Fprintf(w, "[skill] activated %s\n", d.Name) //nolint:errcheck
				}
			case agent.EventWarning:
				if d, ok := ev.Data.(agent.WarningData); ok {
					fmt.Fprintf(w, "[warning] %s\n", d.Message) //nolint:errcheck
				}
			case agent.EventError:
				if d, ok := ev.Data.(agent.ErrorData); ok {
					fmt.Fprintf(w, "[error] %s\n", d.Error) //nolint:errcheck
				}
			}
		}
	}()
	return done
}

// resolveSnapshot loads the session snapshot for the given resume configuration.
func resolveSnapshot(cfg runConfig, stateDir string) (agent.SessionSnapshot, error) {
	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	return cmdutil.ResolveSnapshot(stateDir, id, cfg.resumeLast)
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig, stateDir string) error {
	list, err := agent.ListSessions(stateDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(cfg.stdout, "No saved sessions.") //nolint:errcheck
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
		fmt.Fprintf(cfg.stdout, "%s  %-16s  %-20s  %-20s  turns=%d  %q\n", //nolint:errcheck
			s.ID, s.Model, branch, s.UpdatedAt.Format("2006-01-02 15:04:05"), s.TurnCount, firstInput)
	}
	return nil
}


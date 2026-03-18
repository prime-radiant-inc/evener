package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	minResultRound     int      // --min-result-round (0=no minimum)
	enableReviewerGate bool     // --enable-reviewer-gate
	resultToolName     string   // --result-tool-name override
	reasoningEffort    string   // --reasoning-effort override (or SERF_REASONING_EFFORT)
	contextStrategy    string   // --context-strategy
	exportATIF         string   // --export-atif path
	verbose            bool
	noProjectPrompts   bool
	agentName          string // --agent persona name
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
	// Priority: --state-dir flag > SERF_STATE_DIR env > XDG-computed default.
	stateDir := cfg.stateDir
	if stateDir == "" {
		stateDir = os.Getenv("SERF_STATE_DIR")
	}
	if stateDir == "" {
		originURL := cmdutil.GitOriginURLFromDir(cfg.workDir)
		stateDir = agent.RuntimeDir(originURL, cfg.workDir, "")
	}

	// --list-sessions: print and exit.
	if cfg.listSessions {
		return listSessions(cfg, stateDir)
	}

	// Resolve resume target.
	var meta *agent.SessionMeta
	if cfg.resume != "" || cfg.resumeWith != "" || cfg.resumeLast {
		m, err := resolveSessionMeta(cfg, stateDir)
		if err != nil {
			return err
		}
		meta = &m
	}

	// Determine task.
	task := strings.TrimSpace(cfg.task)
	if meta != nil && cfg.resumeWith == "" && task == "" {
		// --resume without a task: continue with a generic prompt.
		task = "Continue where you left off."
	}
	if task == "" && meta == nil {
		return fmt.Errorf("no task provided")
	}

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
	}

	// Resolve provider: explicit flag > meta > SERF_PROVIDER env var.
	provider := cfg.provider
	if provider == "" && meta != nil {
		provider = meta.ProfileID
	}
	if provider == "" {
		provider = os.Getenv("SERF_PROVIDER")
	}
	if provider == "" {
		return fmt.Errorf("no provider specified: use --provider or set SERF_PROVIDER (openai, anthropic, google)")
	}

	// Resolve model: explicit flag > meta > SERF_MODEL env var.
	model := cfg.model
	if model == "" && meta != nil {
		model = meta.Model
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

	// API call logging — one file per invocation, captures all sessions.
	apiLogPath := filepath.Join(stateDir, "api.jsonl")
	apiLog, apiLogErr := llm.NewAPILogger(apiLogPath)
	if apiLogErr != nil {
		fmt.Fprintf(cfg.stderr, "warning: API logging disabled: %v\n", apiLogErr) //nolint:errcheck
	} else {
		apiLog.SyncInterval = 2 * time.Second
		client.Use(apiLog)
		defer apiLog.Close()
	}

	profile, err := cmdutil.SelectProfile(provider, model)
	if err != nil {
		return err
	}
	env := agent.NewLocalExecutionEnvironment(cfg.workDir)

	var sess *agent.Session
	if meta != nil {
		sess, err = agent.RestoreSessionFromMeta(client, profile, env, *meta, stateDir)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", meta.ID, meta.TurnCount) //nolint:errcheck
	} else {
		sessionCfg := agent.SessionConfig{
			MaxToolRoundsPerInput: cmdutil.MaxRoundsToConfig(cfg.maxRounds),
			MinResultRound:        cfg.minResultRound,
			EnableReviewerGate:    cfg.enableReviewerGate,
			ResultToolName:        cfg.resultToolName,
			StateDir:              stateDir,
			SystemPromptFile:      cfg.systemPrompt,
			SystemPromptAppend:    cfg.systemPromptAppend,
			NoProjectPrompts:      cfg.noProjectPrompts,
			AgentName:             cfg.agentName,
			SkillsDirs:            cfg.skillsDirs,
			MCPConfigFiles:        cfg.mcpConfigs,
			MCPInline:             cfg.mcpServers,
			PluginDirs:            cfg.pluginDirs,
			ContextStrategy:       cfg.contextStrategy,
			ExportATIFPath:        cfg.exportATIF,
			NonInteractive:        true,
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
			case agent.EventPromptLoaded:
				if d, ok := ev.Data.(agent.PromptLoadedData); ok {
					fmt.Fprintf(w, "[prompt] %s (%dB)\n", d.Label, d.Size) //nolint:errcheck
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
			case agent.EventSubmitResult:
				if d, ok := ev.Data.(agent.SubmitResultData); ok {
					fmt.Fprintf(w, "[communicate] %s\n", d.Message) //nolint:errcheck
				}
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

// resolveSessionMeta loads the session meta for the given resume configuration.
func resolveSessionMeta(cfg runConfig, stateDir string) (agent.SessionMeta, error) {
	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	return cmdutil.ResolveSessionMeta(stateDir, id, cfg.resumeLast)
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig, stateDir string) error {
	list, err := agent.ListSessionMetas(stateDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(cfg.stdout, "No saved sessions.") //nolint:errcheck
		return nil
	}
	for _, m := range list {
		branch := m.EnvInfo.GitBranch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(cfg.stdout, "%s  %-16s  %-20s  %-20s  turns=%d\n", //nolint:errcheck
			m.ID, m.Model, branch, m.UpdatedAt.Format("2006-01-02 15:04:05"), m.TurnCount)
	}
	return nil
}


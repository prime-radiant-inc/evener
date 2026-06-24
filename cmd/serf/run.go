package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/kimi_anthropic"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
)

type runConfig struct {
	prompt                    string
	model                     string
	fastCheapModel            string // --fast-cheap-model override for auxiliary side calls
	workDir                   string
	stateDir                  string   // --state-dir override
	systemPrompt              string   // --system-prompt file path
	systemPromptAppend        []string // --system-prompt-append file paths
	maxRounds                 int      // --max-rounds (-1=default, 0=unlimited, >0=limit)
	maxSubagentDepth          int      // --max-subagent-depth (-1=default)
	shareTaskStore            bool     // --share-task-store
	resultToolName            string   // --result-tool-name override
	reasoningEffort           string   // --reasoning-effort override (or SERF_REASONING_EFFORT)
	contextStrategy           string   // --context-strategy
	exportATIF                string   // --export-atif path
	exportATIFProviderHandles string   // --export-atif-provider-handles
	outputSchema              string   // --output-schema: raw JSON schema applied to communicate.output
	verbose                   bool
	noProjectPrompts          bool
	agentName                 string // --agent persona name (default: default)
	stdout                    io.Writer
	stderr                    io.Writer

	skillsDirs                  []string // extra skill directories
	mcpServers                  []string // --mcp inline specs
	mcpConfigs                  []string // --mcp-config file paths
	pluginDirs                  []string // --plugin-dir directories
	systemPromptAsUser          bool     // --system-prompt-as-user
	openAIResponsesContinuation string   // --openai-responses-continuation

	// Resume options.
	resume       string // session ID to resume
	resumeWith   string // session ID whose context to reuse with a new prompt
	resumeLast   bool   // resume the most recent session
	listSessions bool   // print saved sessions and exit
}

// runLoadClient is the injectable hook for tests. Production code calls
// cmdutil.LoadClient; tests may replace this to drive run() with a scripted
// provider while exercising the real CLI/session/tool plumbing.
var runLoadClient = cmdutil.LoadClient

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
	if err := cmdutil.EnsureUserConfigDirs(); err != nil {
		return err
	}
	openAIResponsesContinuation := resolveOpenAIResponsesContinuation(cfg.openAIResponsesContinuation, nil)

	// Compute runtime state directory.
	// Priority: --state-dir flag > SERF_STATE_DIR env > XDG-computed default.
	stateDir := cfg.stateDir
	if stateDir == "" {
		stateDir = envvars.SERFStateDir.Getenv()
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
	var meta *schema.SessionMeta
	if cfg.resume != "" || cfg.resumeWith != "" || cfg.resumeLast {
		m, err := resolveSessionMeta(cfg, stateDir)
		if err != nil {
			return err
		}
		meta = &m
	}

	// Determine prompt.
	prompt := strings.TrimSpace(cfg.prompt)
	if meta != nil && cfg.resumeWith == "" && prompt == "" {
		// --resume without a prompt: continue with a generic prompt.
		prompt = "Continue where you left off."
	}
	if prompt == "" && meta == nil {
		return errors.New("no prompt provided")
	}

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, envvars.SERFReasoningEffort.Getenv())
	if err != nil {
		return err
	}

	resumeProvider := ""
	resumeModel := ""
	if meta != nil {
		resumeProvider = meta.ProfileID
		resumeModel = meta.Model
	}
	var modelRef cmdutil.ModelRef
	if meta != nil {
		modelRef, err = cmdutil.ResolveResumeModelRef(cfg.model, envvars.SERFModel.Getenv(), resumeProvider, resumeModel)
	} else {
		modelRef, err = cmdutil.ResolveModelRef(cfg.model, envvars.SERFModel.Getenv(), "", "")
	}
	if err != nil {
		return err
	}

	client, provCfg, hasProvConfig, err := runLoadClient(llm.WithStateDir(stateDir))
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	closeAPILog, err := cmdutil.AttachAPILogger(client, stateDir, cfg.stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck

	profile, err := buildInitialProfile(provCfg, modelRef, cfg.outputSchema)
	if err != nil {
		return err
	}
	profile, err = applyFastCheapModel(profile, cfg.fastCheapModel, client)
	if err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(cfg.workDir)

	var sess *agent.Session
	baseSessionCfg := agent.SessionConfig{
		MaxToolRoundsPerInput:       cmdutil.MaxRoundsToConfig(cfg.maxRounds),
		ShareTasksWithChildren:      cfg.shareTaskStore,
		ResultToolName:              cfg.resultToolName,
		StateDir:                    stateDir,
		SystemPromptFile:            cfg.systemPrompt,
		SystemPromptAppend:          cfg.systemPromptAppend,
		NoProjectPrompts:            cfg.noProjectPrompts,
		AgentName:                   cfg.agentName,
		SkillsDirs:                  cfg.skillsDirs,
		MCPConfigFiles:              cfg.mcpConfigs,
		MCPInline:                   cfg.mcpServers,
		PluginDirs:                  cfg.pluginDirs,
		ContextStrategy:             cfg.contextStrategy,
		ExportATIFPath:              cfg.exportATIF,
		ExportATIFProviderHandles:   cfg.exportATIFProviderHandles,
		NonInteractive:              true,
		SystemPromptAsUser:          cfg.systemPromptAsUser,
		OpenAIResponsesContinuation: openAIResponsesContinuation,
		ResolveProfile:              cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
	}
	if cfg.maxSubagentDepth >= 0 {
		baseSessionCfg.MaxSubagentDepth = cfg.maxSubagentDepth
	}
	if effort.Set {
		baseSessionCfg.ReasoningEffort = effort.Value
	}
	if meta != nil {
		sess, err = agent.RestoreSessionFromMetaWithConfig(client, profile, env, *meta, agent.RestoreSessionConfig{
			StateDir:                    stateDir,
			ResolveProfile:              baseSessionCfg.ResolveProfile,
			OpenAIResponsesContinuation: openAIResponsesContinuation,
		})
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		if strings.TrimSpace(cfg.model) != "" {
			fmt.Fprintf(cfg.stderr, "[resumed] session %s with model override %s (was %s/%s)\n", meta.ID, modelRef.Qualified(), resumeProvider, resumeModel) //nolint:errcheck
		} else {
			fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", meta.ID, meta.TurnCount) //nolint:errcheck
		}
	} else {
		sess, err = agent.NewSession(client, profile, env, baseSessionCfg)
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

	result, err := sess.ProcessInput(ctx, prompt, nil)
	sess.Close()
	<-done

	if err != nil {
		return err
	}

	fmt.Fprintln(cfg.stdout, result) //nolint:errcheck
	return nil
}

// drainEventsVerbose writes every event as a JSON line (NDJSON) to w.
func drainEventsVerbose(eventCh <-chan events.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for ev := range eventCh {
			enc.Encode(ev) //nolint:errcheck
		}
	}()
	return done
}

// drainEventsHuman writes human-readable status lines to w.
func drainEventsHuman(eventCh <-chan events.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range eventCh {
			switch ev.Kind {
			case events.EventSessionStart:
				if d, ok := ev.Data.(events.SessionStartData); ok && d.Model != "" {
					fmt.Fprintf(w, "[model] %s (%s)\n", d.Model, d.Profile) //nolint:errcheck
				}
			case events.EventPromptLoaded:
				if d, ok := ev.Data.(events.PromptLoadedData); ok {
					fmt.Fprintf(w, "[prompt] %s (%dB)\n", d.Label, d.Size) //nolint:errcheck
				}
			case events.EventAssistantTextEnd:
				if d, ok := ev.Data.(events.AssistantTextEndData); ok {
					if strings.TrimSpace(d.Text) != "" {
						fmt.Fprintf(w, "[assistant] %s\n", d.Text) //nolint:errcheck
					}
					if d.Reasoning != "" {
						fmt.Fprintf(w, "[thinking] (%d chars)\n", len(d.Reasoning)) //nolint:errcheck
					}
					usage := d.Usage
					line := fmt.Sprintf("[usage] in=%d out=%d total=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
					if usage.CacheReadTokens != nil {
						line += fmt.Sprintf(" cache_read=%d", *usage.CacheReadTokens)
					}
					if usage.CacheWriteTokens != nil {
						line += fmt.Sprintf(" cache_write=%d", *usage.CacheWriteTokens)
					}
					fmt.Fprintln(w, line) //nolint:errcheck
				}
			case events.EventToolCallStart:
				if d, ok := ev.Data.(events.ToolCallStartData); ok {
					args := d.ArgumentsJSON
					if len(args) > 100 {
						args = args[:97] + "..."
					}
					fmt.Fprintf(w, "[tool] %s %s\n", d.ToolName, args) //nolint:errcheck
				}
			case events.EventToolCallEnd:
				if d, ok := ev.Data.(events.ToolCallEndData); ok {
					if d.Error != "" {
						fmt.Fprintf(w, "[tool] %s: error\n", d.ToolName) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[tool] %s: done\n", d.ToolName) //nolint:errcheck
					}
				}
			case events.EventCommunicate:
				if d, ok := ev.Data.(events.CommunicateData); ok {
					if d.EndTurn {
						fmt.Fprintf(w, "[communicate:end_turn] %s\n", d.Message) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[communicate] %s\n", d.Message) //nolint:errcheck
					}
				}
			case events.EventPluginLoaded:
				if d, ok := ev.Data.(events.PluginLoadedData); ok {
					fmt.Fprintf(w, "[plugin] loaded %s (%d skills, %d agents, %d mcp)\n", //nolint:errcheck
						d.Name, d.SkillCount, d.AgentCount, d.MCPCount)
				}
			case events.EventHookStart:
				if d, ok := ev.Data.(events.HookStartData); ok {
					fmt.Fprintf(w, "[hook] %s %s (%s)\n", d.Event, d.Matcher, d.HookType) //nolint:errcheck
				}
			case events.EventHookEnd:
				if d, ok := ev.Data.(events.HookEndData); ok {
					fmt.Fprintf(w, "[hook] %s %s done (%dms)\n", d.Event, d.Matcher, d.DurationMS) //nolint:errcheck
				}
			case events.EventSkillActivated:
				if d, ok := ev.Data.(events.SkillActivatedData); ok {
					fmt.Fprintf(w, "[skill] activated %s\n", d.Name) //nolint:errcheck
				}
			case events.EventWarning:
				if d, ok := ev.Data.(events.WarningData); ok {
					fmt.Fprintf(w, "[warning] %s\n", d.Message) //nolint:errcheck
				}
			case events.EventError:
				if d, ok := ev.Data.(events.ErrorData); ok {
					fmt.Fprintf(w, "[error] %s\n", d.Error) //nolint:errcheck
				}
			}
		}
	}()
	return done
}

// resolveSessionMeta loads the session meta for the given resume configuration.
func resolveSessionMeta(cfg runConfig, stateDir string) (schema.SessionMeta, error) {
	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	return cmdutil.ResolveSessionMeta(stateDir, id, cfg.resumeLast)
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig, stateDir string) error {
	list, err := schema.ListSessionMetas(stateDir)
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

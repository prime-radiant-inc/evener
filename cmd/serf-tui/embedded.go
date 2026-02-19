package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/server"
)

// embeddedConfig holds options for the embedded server.
type embeddedConfig struct {
	provider           string
	model              string
	workDir            string
	stateDir           string
	systemPrompt       string
	systemPromptAppend []string
	maxRounds          int
	reasoningEffort    string
	skillsDirs         []string
	mcpServers         []string
	mcpConfigs         []string
	pluginDirs         []string
	resume             string
	resumeLast         bool
}

// embeddedServer wraps an in-process agent session and HTTP server.
type embeddedServer struct {
	addr     string
	srv      *server.Server
	httpSrv  *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	history  []agent.Turn // non-nil when session was restored

	// Mutable session state, protected by mu.
	mu   sync.Mutex
	sess *agent.Session

	// Stored for session recreation on /clear.
	client     *llm.Client
	profile    agent.ProviderProfile
	env        agent.ExecutionEnvironment
	sessionCfg agent.SessionConfig
}

// startEmbedded creates an agent session and HTTP server in-process,
// listening on a random localhost port. Returns the server and its address.
func startEmbedded(ctx context.Context, cfg embeddedConfig) (*embeddedServer, error) {
	wd := cfg.workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	sd := cfg.stateDir
	if sd == "" {
		originURL := cmdutil.GitOriginURLFromDir(wd)
		sd = agent.RuntimeDir(originURL, wd, "")
	}

	prov, err := cmdutil.ResolveProvider(cfg.provider)
	if err != nil {
		return nil, err
	}
	mod, err := cmdutil.ResolveModel(cfg.model)
	if err != nil {
		return nil, err
	}

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return nil, err
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		return nil, fmt.Errorf("LLM client: %w", err)
	}

	profile, err := cmdutil.SelectProfile(prov, mod)
	if err != nil {
		return nil, err
	}

	env := agent.NewLocalExecutionEnvironment(wd)

	sessionCfg := agent.SessionConfig{
		StateDir:              sd,
		SystemPromptFile:      cfg.systemPrompt,
		SystemPromptAppend:    cfg.systemPromptAppend,
		MaxToolRoundsPerInput: cmdutil.MaxRoundsToConfig(cfg.maxRounds),
		SkillsDirs:            cfg.skillsDirs,
		MCPConfigFiles:        cfg.mcpConfigs,
		MCPInline:             cfg.mcpServers,
		PluginDirs:            cfg.pluginDirs,
	}
	if effort.Set {
		sessionCfg.ReasoningEffort = effort.Value
	}

	var sess *agent.Session
	var history []agent.Turn
	if cfg.resume != "" || cfg.resumeLast {
		snap, snapErr := cmdutil.ResolveSnapshot(sd, cfg.resume, cfg.resumeLast)
		if snapErr != nil {
			return nil, snapErr
		}
		history = snap.History
		sess, err = agent.RestoreSession(client, profile, env, snap, sd)
		if err != nil {
			return nil, fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
	} else {
		sess, err = agent.NewSession(client, profile, env, sessionCfg)
		if err != nil {
			return nil, fmt.Errorf("session creation: %w", err)
		}
	}

	srv := server.NewServer(server.ServerConfig{})

	// Listen on random localhost port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	e := &embeddedServer{
		addr:       listener.Addr().String(),
		srv:        srv,
		listener:   listener,
		cancel:     cancel,
		history:    history,
		sess:       sess,
		client:     client,
		profile:    profile,
		env:        env,
		sessionCfg: sessionCfg,
	}

	e.wireSession(sess)

	// Bridge session events to SSE broadcaster.
	go server.Bridge(srv, sess.Events())

	// Input processing loop.
	go e.inputLoop(ctx)

	httpSrv := &http.Server{Handler: srv}
	e.httpSrv = httpSrv
	go httpSrv.Serve(listener) //nolint:errcheck

	return e, nil
}

// wireSession updates server callbacks to point at the given session.
func (e *embeddedServer) wireSession(sess *agent.Session) {
	e.srv.SetCompactFunc(sess.Compact)
	e.srv.SetSteerFunc(sess.Steer)
	e.srv.SetContextPressureFunc(sess.ContextPressure)
	e.srv.SetModelFunc(sess.SetModel)
	e.srv.SetClearFunc(e.clearSession)
	e.srv.SetDetailedStatusFunc(func() server.DetailedStatus {
		return agentToServerStatus(sess.DetailedStatus())
	})
	e.srv.SetListModelsFunc(cmdutil.ListModelsFunc(e.client, e.profile.ID()))
}

// agentToServerStatus converts an agent.DetailedStatus to a server.DetailedStatus.
func agentToServerStatus(ds agent.DetailedStatus) server.DetailedStatus {
	var out server.DetailedStatus

	for _, t := range ds.Tools {
		out.Tools = append(out.Tools, server.ToolInfo{Name: t.Name, Source: t.Source})
	}
	for _, m := range ds.MCP {
		out.MCP = append(out.MCP, server.MCPServerInfo{Name: m.Name, Tools: m.Tools})
	}
	for _, s := range ds.Skills {
		out.Skills = append(out.Skills, server.SkillInfo{Name: s.Name, Description: s.Description})
	}
	for _, p := range ds.Plugins {
		out.Plugins = append(out.Plugins, server.PluginStatusInfo{
			Name:       p.Name,
			Version:    p.Version,
			SkillCount: p.SkillCount,
			AgentCount: p.AgentCount,
			HookCount:  p.HookCount,
			MCPCount:   p.MCPCount,
		})
	}
	if len(ds.Hooks) > 0 {
		out.Hooks = make(map[string]int, len(ds.Hooks))
		for event, count := range ds.Hooks {
			out.Hooks[string(event)] = count
		}
	}
	for _, s := range ds.Subagents {
		out.Subagents = append(out.Subagents, server.SubagentStatusInfo{
			ID:        s.ID,
			Status:    string(s.Status),
			TurnsUsed: s.TurnsUsed,
		})
	}
	out.Agents = ds.Agents

	return out
}

// currentSession returns the current session under lock.
func (e *embeddedServer) currentSession() *agent.Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sess
}

// inputLoop reads from the server's input channel and processes with the current session.
func (e *embeddedServer) inputLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text, ok := <-e.srv.InputCh():
			if !ok {
				return
			}
			sess := e.currentSession()
			e.srv.SetProcessing(true)
			e.srv.SetState("PROCESSING")
			_, processErr := sess.ProcessInput(ctx, text)
			e.srv.SetProcessing(false)
			e.srv.SetState("IDLE")
			if processErr != nil {
				fmt.Fprintf(os.Stderr, "[embedded] error: %v\n", processErr)
			}
		}
	}
}

// clearSession closes the current session and creates a new one with the same config.
func (e *embeddedServer) clearSession(ctx context.Context) error {
	e.mu.Lock()
	oldSess := e.sess
	e.mu.Unlock()

	oldSess.Close()

	newSess, err := agent.NewSession(e.client, e.profile, e.env, e.sessionCfg)
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}

	e.mu.Lock()
	e.sess = newSess
	e.mu.Unlock()

	e.wireSession(newSess)
	go server.Bridge(e.srv, newSess.Events())

	return nil
}

// Close shuts down the embedded server and session.
func (e *embeddedServer) Close() {
	e.cancel()
	e.httpSrv.Close()
	e.currentSession().Close()
}

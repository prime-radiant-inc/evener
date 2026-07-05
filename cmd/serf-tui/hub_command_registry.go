package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

type hubCommandScope uint8

const (
	hubCommandDashboard hubCommandScope = 1 << iota
	hubCommandSession
)

type hubCommandContext struct {
	mode hubMode
	caps hubSessionCapabilities
}

type hubCommandDefinition struct {
	Name               string
	Summary            string
	PaletteLabel       string
	PaletteDetail      string
	Scopes             hubCommandScope
	UnavailableAction  string
	UnavailableSummary string
	Available          func(hubCommandContext) (bool, string)
	Run                func(*hubModel, string) tea.Cmd
}

var hubCommandRegistry = []hubCommandDefinition{
	{
		Name:          "new",
		Summary:       "Open spawn form",
		PaletteLabel:  "/new",
		PaletteDetail: "open spawn form",
		Scopes:        hubCommandDashboard,
		Run: func(m *hubModel, _ string) tea.Cmd {
			m.openSpawnForm()
			if m.client != nil {
				return fetchHubSpawnOptions(m.client, m.spawnDir)
			}
			return nil
		},
	},
	{
		Name:          "refresh",
		Summary:       "Fetch live sessions",
		PaletteLabel:  "/refresh",
		PaletteDetail: "fetch live sessions",
		Scopes:        hubCommandDashboard,
		Run: func(m *hubModel, _ string) tea.Cmd {
			if m.client != nil {
				return fetchHubTree(m.client)
			}
			return nil
		},
	},
	{
		Name:          "upgrade",
		Summary:       "Upgrade installed Serf",
		PaletteLabel:  "/upgrade",
		PaletteDetail: "upgrade installed Serf",
		Scopes:        hubCommandDashboard | hubCommandSession,
		Run: func(m *hubModel, args string) tea.Cmd {
			if m.client == nil {
				err := errors.New("upgrade is not available without a hub client")
				if m.mode == hubModeSession {
					m.recordSessionError(err.Error())
				} else {
					m.err = err
				}
				return nil
			}
			return sendHubUpgrade(m.client, args)
		},
	},
	{
		Name:          "help",
		Summary:       "Show this help",
		PaletteLabel:  "/help",
		PaletteDetail: "show command help",
		Scopes:        hubCommandSession,
	},
	{
		Name:          "dashboard",
		Summary:       "Go to live dashboard",
		PaletteLabel:  "/dashboard",
		PaletteDetail: "go to live dashboard",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, _ string) tea.Cmd {
			m.returnToDashboard()
			return nil
		},
	},
	{
		Name:          "project",
		Summary:       "Show this session's project in the dashboard",
		PaletteLabel:  "/project",
		PaletteDetail: "show this session's project in dashboard",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, _ string) tea.Cmd {
			key, ok := m.projectKeyForSession()
			if !ok {
				m.addSessionSystem("Project is not available for this session.")
				return nil
			}
			m.focusDashboardProject(key)
			return nil
		},
	},
	{
		Name:          "auth",
		Summary:       "Show OpenAI auth status",
		PaletteLabel:  "/auth",
		PaletteDetail: "show OpenAI auth status",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, args string) tea.Cmd {
			return fetchHubAuthStatus(m.client, authProviderArg(args))
		},
	},
	{
		Name:          "login",
		Summary:       "Start OpenAI OAuth login",
		PaletteLabel:  "/login",
		PaletteDetail: "start OpenAI OAuth login",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, args string) tea.Cmd {
			return startHubAuthLogin(m.client, authProviderArg(args))
		},
	},
	{
		Name:          "logout",
		Summary:       "Sign out of OpenAI OAuth",
		PaletteLabel:  "/logout",
		PaletteDetail: "sign out of OpenAI OAuth",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, args string) tea.Cmd {
			return logoutHubAuth(m.client, authProviderArg(args))
		},
	},
	{
		Name:          "tasks",
		Summary:       "Show the agent's task list",
		PaletteLabel:  "/tasks",
		PaletteDetail: "show the agent's task list",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, _ string) tea.Cmd {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return fetchHubTasks(m.client, ref)
		},
	},
	{
		Name:          "agents",
		Summary:       "View the main or subagent transcript",
		PaletteLabel:  "/agents",
		PaletteDetail: "view transcripts",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, _ string) tea.Cmd {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return fetchHubTranscriptTargets(m.client, ref)
		},
	},
	{
		Name:          "goal",
		Summary:       "Set, clear, or check the session's goal",
		PaletteLabel:  "/goal",
		PaletteDetail: "set/clear/status the session goal",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, args string) tea.Cmd {
			return m.runHubGoal(args)
		},
	},
	{
		Name:          "status",
		Summary:       "Show session info and context pressure",
		PaletteLabel:  "/status",
		PaletteDetail: "show live session summary",
		Scopes:        hubCommandSession,
		Run:           fetchCurrentHubStatus,
	},
	{
		Name:          "details",
		Summary:       "Show session details",
		PaletteLabel:  "/details",
		PaletteDetail: "show full metadata and diagnostics",
		Scopes:        hubCommandSession,
		Run:           fetchCurrentHubSession,
	},
	{
		Name:               "interrupt",
		Summary:            "Interrupt the active turn",
		PaletteLabel:       "/interrupt",
		PaletteDetail:      "interrupt the active turn",
		Scopes:             hubCommandSession,
		UnavailableAction:  "interrupt",
		UnavailableSummary: "Interrupt is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.Interrupt }, "source does not advertise interrupt"),
		Run: func(m *hubModel, _ string) tea.Cmd {
			if strings.TrimSpace(m.detail.ActiveTurnID) == "" {
				m.addSessionSystem("Interrupt is not available until an active turn starts.")
				return nil
			}
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubAction(m.client, ref, "interrupt", m.detail.ActiveTurnID)
		},
	},
	{
		Name:               "compact",
		Summary:            "Compact context (free up token space)",
		PaletteLabel:       "/compact",
		PaletteDetail:      "compact context",
		Scopes:             hubCommandSession,
		UnavailableAction:  "compact",
		UnavailableSummary: "Compact is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.Compact }, "source does not advertise compact"),
		Run: func(m *hubModel, _ string) tea.Cmd {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubAction(m.client, ref, "compact", "")
		},
	},
	{
		Name:               "clear",
		Summary:            "Start a new session",
		PaletteLabel:       "/clear",
		PaletteDetail:      "clear current session",
		Scopes:             hubCommandDashboard | hubCommandSession,
		UnavailableAction:  "clear",
		UnavailableSummary: "Clear is not available for this session.",
		Available: func(ctx hubCommandContext) (bool, string) {
			if ctx.mode != hubModeSession {
				return false, "open a session first"
			}
			if !ctx.caps.Clear {
				return false, "source does not advertise clear"
			}
			return true, ""
		},
		Run: func(m *hubModel, _ string) tea.Cmd {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubClear(m.client, ref)
		},
	},
	{
		Name:               "fork",
		Summary:            "Fork selected user turn",
		PaletteLabel:       "/fork",
		PaletteDetail:      "browse and fork a user turn",
		Scopes:             hubCommandSession,
		UnavailableAction:  "fork",
		UnavailableSummary: "Fork is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.Fork }, "source does not advertise fork"),
		Run: func(m *hubModel, _ string) tea.Cmd {
			m.enterSessionBrowse(false)
			m.addSessionSystem("Select a user turn, then press f to fork.")
			return nil
		},
	},
	{
		Name:               "shutdown",
		Summary:            "Stop this resumable session",
		PaletteLabel:       "/shutdown",
		PaletteDetail:      "stop this resumable session",
		Scopes:             hubCommandSession,
		UnavailableAction:  "shutdown",
		UnavailableSummary: "Shutdown is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.Shutdown }, "source does not advertise shutdown"),
		Run: func(m *hubModel, _ string) tea.Cmd {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubAction(m.client, ref, "shutdown", "")
		},
	},
	{
		Name:               "model",
		Summary:            "Switch model (picker) or /model <name>",
		PaletteLabel:       "/model",
		PaletteDetail:      "switch model",
		Scopes:             hubCommandSession,
		UnavailableAction:  "change model",
		UnavailableSummary: "Model change is not available for this session.",
		Available:          capabilityAvailable(func(c hubSessionCapabilities) bool { return c.ChangeModel }, "source does not advertise change model"),
		Run: func(m *hubModel, args string) tea.Cmd {
			model := strings.TrimSpace(args)
			if model == "" {
				if m.client == nil {
					m.addSessionSystem("Model picker is not available without a hub client.")
					return nil
				}
				m.addSessionSystem("Fetching available models...")
				return fetchHubSessionModels(m.client, m.detail.WorkingDir)
			}
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return nil
			}
			return sendHubAction(m.client, ref, model, "")
		},
	},
	{
		Name:          "theme",
		Summary:       "Pick a theme (system/dark/light)",
		PaletteLabel:  "/theme",
		PaletteDetail: "pick a theme",
		Scopes:        hubCommandSession,
		Run: func(m *hubModel, _ string) tea.Cmd {
			picker := tuipick.NewThemePicker()
			m.sessionThemePicker = &picker
			return nil
		},
	},
	{
		Name:          "credentials",
		Summary:       "Manage provider API keys and OAuth sign-in",
		PaletteLabel:  "/credentials",
		PaletteDetail: "manage provider API keys and OAuth sign-in",
		Scopes:        hubCommandDashboard,
		Run: func(m *hubModel, _ string) tea.Cmd {
			panel := launchconfig.NewCredentialsPanel()
			m.credentialsPanel = &panel
			if m.client != nil {
				return launchconfig.CmdInstanceList(m.client)
			}
			return nil
		},
	},
	{
		Name:          "settings",
		Summary:       "Edit hub launch configuration layers",
		PaletteLabel:  "/settings",
		PaletteDetail: "edit hub launch configuration layers",
		Scopes:        hubCommandDashboard,
		Run: func(m *hubModel, _ string) tea.Cmd {
			cwd := m.spawnWorkingDir()
			p := launchconfig.NewLaunchSettingsPanel(m.client, cwd)
			m.launchSettingsPanel = &p
			return p.InitialCmd()
		},
	},
	{
		Name:          "plugins",
		Summary:       "Manage plugin marketplaces and installed plugins",
		PaletteLabel:  "/plugins",
		PaletteDetail: "manage plugin marketplaces and installed plugins",
		Scopes:        hubCommandDashboard,
		Run: func(m *hubModel, _ string) tea.Cmd {
			panel := launchconfig.NewPluginsPanel()
			m.pluginsPanel = &panel
			if m.client != nil {
				return tea.Batch(launchconfig.CmdMarketplaceList(m.client), launchconfig.CmdPluginList(m.client))
			}
			return nil
		},
	},
	{
		Name:          "quit",
		Summary:       "Exit serf-tui",
		PaletteLabel:  "/quit",
		PaletteDetail: "exit serf-tui",
		Scopes:        hubCommandDashboard | hubCommandSession,
		Run: func(_ *hubModel, _ string) tea.Cmd {
			return tea.Quit
		},
	},
}

func capabilityAvailable(check func(hubSessionCapabilities) bool, reason string) func(hubCommandContext) (bool, string) {
	return func(ctx hubCommandContext) (bool, string) {
		if check(ctx.caps) {
			return true, ""
		}
		return false, reason
	}
}

func fetchCurrentHubSession(m *hubModel, _ string) tea.Cmd {
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return nil
	}
	m.sessionDetailsRequested = true
	return fetchHubSession(m.client, ref)
}

func fetchCurrentHubStatus(m *hubModel, _ string) tea.Cmd {
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return nil
	}
	return fetchHubStatus(m.client, ref)
}

func hubCommandByName(name string) (hubCommandDefinition, bool) {
	name = strings.TrimSpace(name)
	for _, command := range hubCommandRegistry {
		if command.Name == name {
			return command, true
		}
	}
	return hubCommandDefinition{}, false
}

func hubCommandsForScope(scope hubCommandScope) []hubCommandDefinition {
	commands := make([]hubCommandDefinition, 0, len(hubCommandRegistry))
	for _, command := range hubCommandRegistry {
		if command.Scopes&scope != 0 {
			commands = append(commands, command)
		}
	}
	return commands
}

func hubCommandAvailable(command hubCommandDefinition, ctx hubCommandContext) (bool, string) {
	if command.Available == nil {
		return true, ""
	}
	return command.Available(ctx)
}

func runHubCommandDefinition(m *hubModel, command hubCommandDefinition, args string) tea.Cmd {
	if command.Name == "help" {
		m.addSessionSystem(hubSlashCommandHelp(m.detail.Capabilities))
		return nil
	}
	if command.Run == nil {
		return nil
	}
	return command.Run(m, args)
}

func hubCommandHelp(caps hubSessionCapabilities) string {
	ctx := hubCommandContext{mode: hubModeSession, caps: caps}
	lines := []string{"Available commands:"}
	for _, command := range hubCommandsForScope(hubCommandSession) {
		available, _ := hubCommandAvailable(command, ctx)
		if !available {
			continue
		}
		lines = append(lines, fmt.Sprintf("  /%-9s %s", command.Name, command.Summary))
	}
	lines = append(lines, "", "Keys:")
	if caps.Send {
		lines = append(lines, "  enter            Send message")
	}
	lines = append(lines,
		"  alt+enter        New line in input",
		"  ctrl+j           New line in input (alternative)",
		"  esc              Browse transcript / select turns",
		"  pgup             Browse transcript and page up",
		"  esc / i          Return from browse to compose",
	)
	if caps.Fork {
		lines = append(lines, "  f                Fork selected user turn in browse")
	}
	lines = append(lines,
		"  ctrl+o           Go to live dashboard",
		"  tab / enter      Expand/collapse focused tool call",
	)
	return strings.Join(lines, "\n")
}

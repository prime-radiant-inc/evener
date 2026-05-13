package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/server"
)

type inputSentMsg struct{ err error }
type steerSentMsg struct{ err error }
type compactDoneMsg struct{ err error }
type transcriptRefreshMsg struct{}
type asyncMsg struct{ msg tea.Msg }

func waitForAsync(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return asyncMsg{msg: msg}
	}
}

// parseSlashCommand returns the command name and arguments if the input starts
// with a slash command. Returns ("", "") if not a slash command.
func parseSlashCommand(input string) (cmd, args string) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", ""
	}
	parts := strings.SplitN(input[1:], " ", 2)
	cmd = parts[0]
	if len(parts) > 1 {
		args = parts[1]
	}
	return cmd, args
}

// slashCommandHelp returns the help text for all available slash commands.
func slashCommandHelp() string {
	return strings.Join([]string{
		"Available commands:",
		"  /help      Show this help",
		"  /compact   Compact context (free up token space)",
		"  /status    Show session info and context pressure",
		"  /tasks     Show the agent's task list",
		"  /agents    View the main or subagent transcript",
		"  /model     Switch model (picker) or /model <name>",
		"  /auth      Open provider auth actions",
		"  /theme     Pick a theme (dark/light)",
		"  /clear     Start a new session",
		"  /dashboard Go to live dashboard",
		"  /project   Go to this session's project",
		"  /quit      Exit the TUI",
		"",
		"Keys:",
		"  enter            Send message",
		"  alt+enter        New line in input",
		"  ctrl+j           New line in input (alternative)",
		"  esc              Browse transcript / select turns",
		"  pgup             Browse transcript and page up",
		"  esc / i          Return from browse to compose",
		"  f                Fork selected user turn in browse",
		"  ctrl+o           Go to live dashboard",
		"  tab / enter      Expand/collapse focused tool call",
	}, "\n")
}

func hubSlashCommandHelp(caps hubSessionCapabilities) string {
	lines := []string{
		"Available commands:",
		"  /help      Show this help",
	}
	if caps.Compact {
		lines = append(lines, "  /compact   Compact context (free up token space)")
	}
	lines = append(lines,
		"  /status    Show session info and context pressure",
		"  /tasks     Show the agent's task list",
	)
	if caps.ChangeModel {
		lines = append(lines, "  /model     Switch model with /model <name>")
	}
	if caps.Clear {
		lines = append(lines, "  /clear     Start a new session")
	}
	if caps.Shutdown {
		lines = append(lines, "  /shutdown  Stop this resumable session")
	}
	lines = append(lines,
		"  /dashboard Go to live dashboard",
		"  /project   Go to this session's project",
		"",
		"Keys:",
	)
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

func sendInput(addr, text string) tea.Cmd {
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"text": text})
		url := fmt.Sprintf("http://%s/input", addr)
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return inputSentMsg{err}
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusConflict {
			return inputSentMsg{fmt.Errorf("session is busy")}
		}
		if resp.StatusCode != http.StatusAccepted {
			return inputSentMsg{fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		return inputSentMsg{}
	}
}

func sendSteer(addr, text string) tea.Cmd {
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"text": text})
		url := fmt.Sprintf("http://%s/steer", addr)
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return steerSentMsg{err}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return steerSentMsg{fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		return steerSentMsg{}
	}
}

func sendCompact(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/compact", addr)
		resp, err := http.Post(url, "", nil)
		if err != nil {
			return compactDoneMsg{err}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return compactDoneMsg{fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		return compactDoneMsg{}
	}
}

type statusResult struct {
	info server.StatusInfo
	err  error
}

type transcriptTargetsResult struct {
	info server.StatusInfo
	err  error
}

func fetchStatus(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/status", addr)
		resp, err := http.Get(url)
		if err != nil {
			return statusResult{err: err}
		}
		defer resp.Body.Close()
		var info server.StatusInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return statusResult{err: err}
		}
		return statusResult{info: info}
	}
}

func fetchTranscriptTargets(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/status", addr)
		resp, err := http.Get(url)
		if err != nil {
			return transcriptTargetsResult{err: err}
		}
		defer resp.Body.Close()
		var info server.StatusInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return transcriptTargetsResult{err: err}
		}
		return transcriptTargetsResult{info: info}
	}
}

type tasksResult struct {
	tasks []agent.Task
	err   error
}

func fetchTasks(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/tasks", addr)
		resp, err := http.Get(url)
		if err != nil {
			return tasksResult{err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return tasksResult{err: fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		var tasks []agent.Task
		if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
			return tasksResult{err: err}
		}
		return tasksResult{tasks: tasks}
	}
}

type modelDoneMsg struct{ err error }
type clearDoneMsg struct{ err error }

func sendModel(addr, model string) tea.Cmd {
	return func() tea.Msg {
		body, _ := json.Marshal(map[string]string{"model": model})
		url := fmt.Sprintf("http://%s/model", addr)
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return modelDoneMsg{err}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return modelDoneMsg{fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		return modelDoneMsg{}
	}
}

func sendClear(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/clear", addr)
		resp, err := http.Post(url, "", nil)
		if err != nil {
			return clearDoneMsg{err}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return clearDoneMsg{fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		return clearDoneMsg{}
	}
}

func sendInterrupt(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/interrupt", addr)
		resp, err := http.Post(url, "", nil)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

type modelsResult struct {
	models []modelPickerItem
	err    error
}

func fetchModels(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/models", addr)
		resp, err := http.Get(url)
		if err != nil {
			return modelsResult{err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return modelsResult{err: fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		var result struct {
			Models []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return modelsResult{err: err}
		}
		items := make([]modelPickerItem, len(result.Models))
		for i, m := range result.Models {
			display := m.DisplayName
			if display == "" {
				display = m.ID
			}
			items[i] = modelPickerItem{id: m.ID, display: display}
		}
		return modelsResult{models: items}
	}
}

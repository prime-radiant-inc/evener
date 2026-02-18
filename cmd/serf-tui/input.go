package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type inputSentMsg struct{ err error }
type compactDoneMsg struct{ err error }

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

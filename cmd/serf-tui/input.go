package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
)

type inputSentMsg struct{ err error }

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

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHubCommandRegistryHasQuit(t *testing.T) {
	cmd, ok := hubCommandByName("quit")
	if !ok {
		t.Fatal("registry missing /quit")
	}
	if cmd.Scopes&hubCommandDashboard == 0 {
		t.Error("/quit should be available in dashboard scope")
	}
	if cmd.Scopes&hubCommandSession == 0 {
		t.Error("/quit should be available in session scope")
	}
	if cmd.Run == nil {
		t.Fatal("/quit has no Run")
	}
	gotMsg := cmd.Run(nil, "")()
	if _, ok := gotMsg.(tea.QuitMsg); !ok {
		t.Fatalf("/quit Run should produce tea.QuitMsg, got %T", gotMsg)
	}
}

func TestHubSlashCommandHelpListsQuit(t *testing.T) {
	help := hubCommandHelp(hubSessionCapabilities{Send: true})
	if !strings.Contains(help, "/quit") {
		t.Fatalf("help should advertise /quit:\n%s", help)
	}
}

package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
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

func TestHubSlashCommandHelpDescribesBrowseMessages(t *testing.T) {
	help := hubCommandHelp(hubSessionCapabilities{Send: true})
	if !strings.Contains(help, "Browse transcript / select messages") {
		t.Fatalf("help should describe browse message selection:\n%s", help)
	}
	if strings.Contains(help, "Browse transcript / select turns") {
		t.Fatalf("help should not describe browse rows as turns:\n%s", help)
	}
}

func TestVisionModelCommandRegistered(t *testing.T) {
	cmd, ok := hubCommandByName("vision-model")
	if !ok {
		t.Fatal("no /vision-model command in the registry")
	}
	if cmd.PaletteLabel != "/vision-model" {
		t.Fatalf("PaletteLabel = %q", cmd.PaletteLabel)
	}
	available, _ := cmd.Available(hubCommandContext{mode: hubModeSession, caps: hubSessionCapabilities{ChangeVisionModel: true}})
	if !available {
		t.Fatal("command must be available when the capability is advertised")
	}
	available, _ = cmd.Available(hubCommandContext{mode: hubModeSession})
	if available {
		t.Fatal("command must gate on ChangeVisionModel")
	}
}

func TestSendHubVisionModelAction(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.ThreadVisionModelSetParams
	appserver.HandleTyped(app.Router(), appwire.MethodThreadVisionModelSet, func(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		got = params
		return appwire.EmptyResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	ref := appwire.Ref{SourceID: "local", ThreadID: "th_1"}
	for _, setting := range []string{"", "off", "anthropic/claude-x"} {
		msg := sendHubVisionModelAction(client, ref, setting)()
		actionMsg, ok := msg.(hubActionMsg)
		if !ok || actionMsg.err != nil {
			t.Fatalf("setting %q: msg=%T err=%v", setting, msg, actionMsg.err)
		}
		if actionMsg.action != "vision-model" {
			t.Fatalf("setting %q: action = %q, want vision-model", setting, actionMsg.action)
		}
		if got.Ref != ref.String() || got.VisionModel != setting {
			t.Fatalf("setting %q: params = %+v, want ref %q and unchanged VisionModel", setting, got, ref.String())
		}
	}
}

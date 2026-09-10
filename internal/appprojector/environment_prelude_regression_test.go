package appprojector

import (
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"testing"
)

func TestEnvironmentDoesNotEndPreludeBeforeDeferredHookWarning(t *testing.T) {
	p := NewAppEventProjector("env-prelude", "local:env-prelude")
	reserved := p.ReserveTurnID()
	env := p.Project(events.SessionEvent{Kind: events.EventEnvironment, Data: events.EnvironmentData{TurnID: "turn_environment", Text: "context"}})
	if got := notificationThreadItem(t, env, appwire.NotifyItemCompleted).TurnID; got != "turn_environment" {
		t.Fatalf("environment turn=%q", got)
	}
	hook := p.Project(events.SessionEvent{Kind: events.EventHookEnd, Data: events.HookEndData{Event: "SessionStart", HookType: "command", PluginName: "resume", ExitCode: 0}})
	if got := notificationTurn(t, hook, appwire.NotifyTurnCompleted).Items[0].TurnID; got != appwire.SystemPreludeTurnID {
		t.Fatalf("hook turn=%q want prelude %q", got, appwire.SystemPreludeTurnID)
	}
	user := p.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "input"}})
	if got := notificationThreadItem(t, user, appwire.NotifyItemCompleted).TurnID; got != reserved {
		t.Fatalf("user turn=%q want reservation %q", got, reserved)
	}
}

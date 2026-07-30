package main

import (
	"sort"
	"testing"

	"primeradiant.com/serf/appwire"
)

// notifyMethodsDeliberatelyIgnored lists wire notifications applyHubNotification
// does NOT dispatch on, each because the TUI has no surface that would change.
// Membership is a decision, not a backlog: moving one out of this list means
// giving it a case.
var notifyMethodsDeliberatelyIgnored = []string{
	// Tree/dashboard shape and attention state are re-fetched wholesale by the
	// dashboard's own poll rather than folded from a push.
	appwire.NotifySerfTreeChanged,
	appwire.NotifySerfAttentionChanged,
	appwire.NotifyThreadStarted,
	appwire.NotifyThreadClosed,
	appwire.NotifyThreadNameChanged,
	// Tasks render from fetchHubTasks, not from the push.
	appwire.NotifySerfTaskUpdated,
	// Resync is handled by re-reading the thread, not by a transcript fold.
	appwire.NotifySerfThreadResync,
	// The TUI surfaces escalation REQUESTS; a resolution simply removes the
	// prompt it already cleared locally when the user answered.
	appwire.NotifySerfSandboxEscalationResolved,
}

// kata e79v: serf/thread/modelRetry was added to the catalog and the TUI ignored
// it silently, so a rate-limited session kept looking wedged in one client after
// being fixed in the other. Nothing failed, because notifyMethods reads like an
// inventory of wire notifications but is really a hand-maintained list of what
// the TUI already dispatches on.
//
// This makes the catalog and the TUI's response to it agree by construction.
// What it proves is narrow and worth stating: every notification is ACCOUNTED
// for, either dispatched or explicitly waived. It cannot prove a dispatched
// method is handled correctly — only that adding one to appwire.Notifications
// is a decision someone had to make, rather than a silent no-op.
func TestEveryWireNotificationIsHandledOrExplicitlyIgnored(t *testing.T) {
	accounted := map[string]bool{}
	for _, method := range notifyMethods {
		accounted[method] = true
	}
	for _, method := range notifyMethodsDeliberatelyIgnored {
		if accounted[method] {
			t.Errorf("%s is listed as both dispatched and deliberately ignored", method)
		}
		accounted[method] = true
	}

	var unaccounted []string
	for _, spec := range appwire.Notifications {
		if !accounted[spec.Name] {
			unaccounted = append(unaccounted, spec.Name)
		}
	}
	sort.Strings(unaccounted)
	if len(unaccounted) > 0 {
		t.Errorf("wire notifications neither dispatched nor explicitly ignored by the TUI: %v\n"+
			"Give each a case in applyHubNotification (and add it to notifyMethods), or add it to "+
			"notifyMethodsDeliberatelyIgnored with the reason it needs no TUI surface.", unaccounted)
	}
}

// The converse: a method listed here that the catalog no longer publishes is
// dead weight that outlives the notification it names.
func TestTUINotificationListsOnlyNameRealWireNotifications(t *testing.T) {
	catalog := map[string]bool{}
	for _, spec := range appwire.Notifications {
		catalog[spec.Name] = true
	}
	for _, method := range append(append([]string{}, notifyMethods...), notifyMethodsDeliberatelyIgnored...) {
		if !catalog[method] {
			t.Errorf("%q is not in appwire.Notifications", method)
		}
	}
}

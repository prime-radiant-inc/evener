package hub

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// maxActivityReadRefs bounds an explicit refs filter: a Board reads with no
// filter, and a session screen with one ref.
const maxActivityReadRefs = 500

func registerActivityReadHandler(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerActivityRead, func(ctx context.Context, params appwire.ActivityReadParams) (appwire.ActivityReadResponse, error) {
		return hubActivityRead(ctx, cfg, sources, params, time.Now())
	})
}

// hubActivityRead answers evener/activity/read (S5): the live top-level
// sessions of this hub's roster, filtered to params.Refs when it names any.
// It never reads navigation and never changes it.
func hubActivityRead(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ActivityReadParams, now time.Time) (appwire.ActivityReadResponse, error) {
	if len(params.Refs) > maxActivityReadRefs {
		return appwire.ActivityReadResponse{}, appwire.InvalidParams(fmt.Sprintf("refs names at most %d sessions", maxActivityReadRefs))
	}
	wanted := make(map[string]struct{}, len(params.Refs))
	for _, raw := range params.Refs {
		ref, err := hubapi.ParseRef(raw)
		if err != nil {
			return appwire.ActivityReadResponse{}, appwire.InvalidParams("refs must be session refs: " + err.Error())
		}
		wanted[ref.String()] = struct{}{}
	}
	keep := func(ref string) bool {
		_, ok := wanted[ref]
		return len(wanted) == 0 || ok
	}
	sessions := []appwire.SessionActivity{}
	if cfg.Roster != nil {
		for _, entry := range cfg.Roster.List() {
			if activity, ok := localSessionActivity(entry, now); ok && keep(activity.Ref) {
				sessions = append(sessions, activity)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Ref < sessions[j].Ref })
	return appwire.ActivityReadResponse{Sessions: sessions}, nil
}

// localSessionActivity is one live root's activity as the hub reads it now.
// Only a live, uncrashed root whose daemon reported a sample has one.
func localSessionActivity(entry hubcore.LiveEntry, now time.Time) (appwire.SessionActivity, bool) {
	if entry.Crashed || entry.SessionID == "" || entry.Activity == nil {
		return appwire.SessionActivity{}, false
	}
	activity := appwire.SessionActivity{
		Ref:              hubcore.LiveRowRef(entry),
		Minutes:          slices.Clone(entry.Activity.Minutes),
		RunningSubagents: runningSubagents(entry),
	}
	// Jesse's ruling: an agent waiting on subagents is never stuck. A subagent
	// inside one long model call emits nothing for minutes, so the tree's quiet
	// clock cannot keep that promise alone; the quiet time is withheld outright
	// while any subagent runs, and while the session is not working.
	if hubcore.NormalizeState(entry.Status) == "active" && activity.RunningSubagents == 0 {
		quiet := max(now.Sub(time.UnixMilli(entry.Activity.LastActivityAt)).Milliseconds(), 0)
		activity.QuietForMS = &quiet
	}
	return activity, true
}

// runningSubagents counts a root's listed descendants whose own status is
// active, by the rule its tree rows use (runningSubagentState in
// hubcore/tree.go): a descendant with no reported state does not count.
func runningSubagents(entry hubcore.LiveEntry) int {
	count := 0
	for _, id := range entry.RunningSubagentIDs {
		if hubcore.NormalizeState(entry.RunningSubagentStates[id]) == "active" {
			count++
		}
	}
	return count
}

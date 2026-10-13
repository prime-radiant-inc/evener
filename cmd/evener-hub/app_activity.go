package hub

import (
	"context"
	"fmt"
	"sort"
	"sync"
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
// sessions of this hub's roster and of its attached hosts, filtered to
// params.Refs when it names any. It never reads navigation and never changes
// it.
func hubActivityRead(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ActivityReadParams, now time.Time) (appwire.ActivityReadResponse, error) {
	if len(params.Refs) > maxActivityReadRefs {
		return appwire.ActivityReadResponse{}, appwire.InvalidParams(fmt.Sprintf("refs names at most %d sessions", maxActivityReadRefs))
	}
	wanted := make(map[string]struct{}, len(params.Refs))
	refsByHost := make(map[string][]string)
	for _, raw := range params.Refs {
		ref, err := hubapi.ParseRef(raw)
		if err != nil {
			return appwire.ActivityReadResponse{}, appwire.InvalidParams("refs must be session refs: " + err.Error())
		}
		wanted[ref.String()] = struct{}{}
		refsByHost[ref.HostID] = append(refsByHost[ref.HostID], ref.String())
	}
	keep := func(ref string) bool {
		_, ok := wanted[ref]
		return len(wanted) == 0 || ok
	}
	_, askedLocal := refsByHost["local"]
	sessions := []appwire.SessionActivity{}
	if cfg.Roster != nil && (len(wanted) == 0 || askedLocal) {
		for _, entry := range cfg.Roster.List() {
			if activity, ok := localSessionActivity(entry, now); ok && keep(activity.Ref) {
				sessions = append(sessions, activity)
			}
		}
	}
	for _, activity := range remoteSessionActivity(ctx, cfg, sources, refsByHost) {
		if !keep(activity.Ref) {
			continue
		}
		// A host's answer is relayed, not trusted: its rows carry the row text
		// this controller allows, whatever version of the code that host runs.
		activity.LatestIntent = appwire.Excerpt(activity.LatestIntent, appwire.MaxIntentRunes)
		// A moved time no seen mark could echo (negative, or further ahead of
		// this hub's clock than seen/set accepts) is dropped, not the row.
		if activity.LastMovedAt < 0 || activity.LastMovedAt > now.Add(maxSeenThroughLead).UnixMilli() {
			activity.LastMovedAt = 0
		}
		sessions = append(sessions, activity)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Ref < sessions[j].Ref })
	return appwire.ActivityReadResponse{Sessions: sessions}, nil
}

// localSessionActivity is one live root's activity as the hub reads it now.
// Only a live, uncrashed root whose daemon reported a sample has one. The
// activity takes entry's minutes as they are: Roster.List hands each caller
// its own CloneLiveEntry copy.
func localSessionActivity(entry hubcore.LiveEntry, now time.Time) (appwire.SessionActivity, bool) {
	if entry.Crashed || entry.SessionID == "" || entry.Activity == nil {
		return appwire.SessionActivity{}, false
	}
	// RunningSubagents is the root's subagent tally, the count its Live row
	// shows: a subagent is running while its run is open, whatever its own
	// status says (a running subagent asking the user reports awaiting).
	activity := appwire.SessionActivity{
		Ref:              hubcore.LiveRowRef(entry),
		Minutes:          entry.Activity.Minutes,
		RunningSubagents: entry.Subagents.Running,
		LatestIntent:     appwire.Excerpt(entry.Activity.LatestIntent, appwire.MaxIntentRunes),
		LastMovedAt:      entry.Activity.LastMovedAt,
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

// remoteActivityReadBudget bounds one host's answer. A host answers from its
// own roster in memory, so anything longer is a stalled channel: that host's
// sessions are left out of this read, and a client keeps its fallback for them
// until the next poll.
const remoteActivityReadBudget = 3 * time.Second

// remoteSessionActivityReader is the source capability the read fans out to:
// an attached host hub answers for its own live sessions.
type remoteSessionActivityReader interface {
	ReadSessionActivity(ctx context.Context, params appwire.ActivityReadParams) (appwire.ActivityReadResponse, error)
}

// remoteSessionActivity asks every attached host refsByHost names (every host
// when it names none) for the refs it lists, in parallel, each bounded by
// remoteActivityReadBudget. A host that fails or times out contributes
// nothing; an unattached host is skipped without a call, never dialed.
func remoteSessionActivity(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, refsByHost map[string][]string) []appwire.SessionActivity {
	if sources == nil {
		return nil
	}
	remoteHosts := remoteHostNames(cfg)
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sessions []appwire.SessionActivity
	)
	for _, source := range sources.All() {
		id := source.ID()
		reader, ok := source.(remoteSessionActivityReader)
		hostRefs, named := refsByHost[id]
		if !ok || (len(refsByHost) > 0 && !named) || !remoteSourceAttached(cfg, remoteHosts, id) {
			continue
		}
		wg.Go(func() {
			hostCtx, cancel := context.WithTimeout(ctx, remoteActivityReadBudget)
			defer cancel()
			response, err := reader.ReadSessionActivity(hostCtx, appwire.ActivityReadParams{Refs: hostRefs})
			if err != nil {
				return
			}
			mu.Lock()
			sessions = append(sessions, response.Sessions...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return sessions
}

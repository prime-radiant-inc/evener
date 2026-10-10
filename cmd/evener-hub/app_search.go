package hub

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const (
	searchPastLimit = 20
	// searchInSessionsLimit bounds the sessions the In sessions group lists
	// (S14), as searchPastLimit bounds the ended sessions.
	searchInSessionsLimit = 20
	// searchHitsPerSession bounds each listed session's message hits.
	searchHitsPerSession = 3
)

// hubSearch answers evener/search: the live sessions and the ended ones whose
// ID, title or prompt match (each session once, live when it is live), and,
// with a message index, the sessions whose messages match (S14). Every group
// keeps only the sessions params.Scope admits, and every result says whether
// the rail files it as archived.
func hubSearch(ctx context.Context, cfg hubcore.WebConfig, params appwire.SearchParams, now time.Time) (appwire.SearchResponse, error) {
	scope := params.Scope
	if scope == "" {
		scope = appwire.SearchScopeAll
	}
	if scope != appwire.SearchScopeAll && scope != appwire.SearchScopeLive && scope != appwire.SearchScopeArchived {
		return appwire.SearchResponse{}, appwire.InvalidParams(`scope must be "all", "live" or "archived"`)
	}
	logf := hubLogfFor(cfg)
	decisions, err := searchDecisions(cfg)
	if err != nil {
		if scope != appwire.SearchScopeAll {
			// Live and Archived filter on the Archived flag; answering them
			// from empty (unavailable) decisions while still claiming the
			// scope was applied would misreport which sessions are archived.
			// All only decorates the flag, so it can still degrade below.
			return appwire.SearchResponse{}, err
		}
		// A broken archive store must not take down ID/title/prompt search,
		// which never depended on one: degrade to "nothing archived by
		// decision" rather than failing the whole query.
		logf("search: archive decisions: %v", err)
		decisions = map[hubcore.ArchiveKey]bool{}
	}
	resp := appwire.SearchResponse{Live: []appwire.SearchResult{}, Past: []appwire.SearchResult{}, Scope: scope}
	q := strings.ToLower(strings.TrimSpace(params.Query))
	var entries []hubcore.LiveEntry
	if cfg.Roster != nil {
		entries = cfg.Roster.List()
		sortLiveForSearch(entries, cfg.Past)
	}
	// A live session's meta sits in the past index too, so a prompt or
	// working-directory match there lists it here, live. Resolve every live
	// session's own past-index match in one lookup, ahead of the bounded Past
	// fetch, so it is found however far back its entry sits and one search costs
	// one query rather than one per live session (#2873).
	var pastMatched map[string]bool
	if cfg.Past != nil && q != "" {
		ids := make([]string, 0, len(entries))
		for _, le := range entries {
			if le.SessionID != "" {
				ids = append(ids, le.SessionID)
			}
		}
		pastMatched = cfg.Past.MatchIDs(ids, q)
	}
	// Every live session's result, in the Live order, built once for both
	// the Live group and the In sessions group.
	var live []appwire.SearchResult
	isLive := map[string]bool{}
	if cfg.Roster != nil {
		// A subagent is not a navigation row: the roots-only tree gives one no
		// top-level row and lets an orphan (its parent not live) vanish (#3082).
		// The Live group mirrors the Board's Live section, so it omits subagents
		// too, the same way it omits an archived session. Without this, a
		// subagent's raw turn-ended "awaiting" state (its normal resting state,
		// with no real ask_pending) reached the palette as a needs-you dot it
		// does not need (#2574). A subagent is one its meta marks, or one a live
		// entry reports as its running child — the same `isSubagent` rule the
		// tree builds rows by.
		roots := hubcore.NewRootIndex(nil)
		if cfg.Past != nil {
			roots = cfg.Past.RootIndex()
		}
		running := hubcore.RunningSubagentIDs(entries)
		for _, le := range entries {
			if le.SessionID == "" {
				continue
			}
			isLive[le.SessionID] = true
			result := liveSearchResult(cfg, le, decisions, now)
			live = append(live, result)
			if q != "" && !strings.Contains(strings.ToLower(le.SessionID), q) && !strings.Contains(strings.ToLower(result.Title), q) && !pastMatched[le.SessionID] {
				continue
			}
			if roots.IsSubagent(le.SessionID) || running[le.SessionID] {
				continue
			}
			if searchScopeAdmits(scope, result.Archived, true) {
				resp.Live = append(resp.Live, result)
			}
		}
	}
	// The Past group's fetch filters before the limit cuts, so the scope still
	// applies over the whole index and newer out-of-scope or live matches cannot
	// crowd an older in-scope match out of the page. It returns at most
	// searchPastLimit entries, so the whole match set no longer lands in the
	// caller's hands on every query, even an empty one (#2873). A live session's
	// own prompt match is answered separately above, so it never depended on this
	// fetch's width.
	if cfg.Past != nil {
		pastMatches := cfg.Past.SearchAdmitted(q, searchPastLimit, func(e hubcore.PastEntry) bool {
			return !isLive[e.Meta.ID] && searchScopeAdmits(scope, pastArchived(e, decisions, now), false)
		})
		for _, e := range pastMatches {
			resp.Past = append(resp.Past, pastSearchResult(e, decisions, now))
		}
	}
	resp.InSessions, err = searchInSessions(ctx, cfg, params.Query, scope, live, decisions, now)
	if err != nil {
		// Same reasoning as the archive store above: a flaky message index
		// costs the In sessions group, not the whole query.
		logf("search: message index: %v", err)
		resp.InSessions = nil
	}
	return resp, nil
}

// searchDecisions is the hub's archive decisions, or none without a store.
func searchDecisions(cfg hubcore.WebConfig) (map[hubcore.ArchiveKey]bool, error) {
	if cfg.Archive == nil {
		return map[hubcore.ArchiveKey]bool{}, nil
	}
	return cfg.Archive.Decisions()
}

// searchScopeAdmits reports whether scope keeps a result with this archived
// flag: Live keeps what the Board's Live section holds, live and not archived;
// Archived keeps every archived session.
func searchScopeAdmits(scope string, archived, live bool) bool {
	switch scope {
	case appwire.SearchScopeLive:
		return live && !archived
	case appwire.SearchScopeArchived:
		return archived
	default:
		return true
	}
}

func liveSearchResult(cfg hubcore.WebConfig, le hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	// A live session with no meta yet has no last activity to age.
	lastActivity := now
	// The roster never carries Project (only the Board's tree-building path
	// resolves one, onto its own local copy search never sees), so
	// LiveSessionArchived would always see it empty and skip the project
	// decision. Fall back to the past entry's state directory, named by its
	// project's ID, the same way pastSearchResult resolves a past entry's.
	archiveEntry := le
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(le.SessionID); ok {
			lastActivity = hubcore.OrderUpdatedAt(pe.Meta.UpdatedAt, pe.Meta.CreatedAt)
			if archiveEntry.Project.ID == "" {
				if id, ok := stateDirProjectID(pe.StateDir); ok {
					archiveEntry.Project.ID = id
				}
			}
		}
	}
	return appwire.SearchResult{
		ID:              le.SessionID,
		Title:           liveTitle(le.SessionID, le, cfg.Past),
		State:           hubcore.NormalizeState(le.Status),
		Project:         filepath.Base(le.WorkingDir),
		Age:             "now",
		Ref:             hubRefFromTreeNodeID(le.SessionID).String(),
		AskPending:      le.PendingAsk,
		ApprovalPending: le.PendingEscalation,
		Archived:        hubcore.LiveSessionArchived(decisions, archiveEntry, lastActivity, now),
	}
}

func pastSearchResult(e hubcore.PastEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	return appwire.SearchResult{
		ID:       e.Meta.ID,
		Title:    searchPastTitle(e),
		State:    "ended",
		Project:  filepath.Base(e.Meta.EnvInfo.WorkingDir),
		Age:      hubcore.AgeString(e.Meta.UpdatedAt),
		Ref:      hubRefFromTreeNodeID(e.Meta.ID).String(),
		Archived: pastArchived(e, decisions, now),
	}
}

// pastArchived is a past entry's archived flag, the one pastSearchResult and
// the Past group's scope filter share so a fetch can admit by scope without
// building the whole result.
func pastArchived(e hubcore.PastEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) bool {
	// A past entry's state directory is named by its project's ID, but only
	// when that basename is well formed: the navigation tree skips a
	// malformed one, so search skips the project decision too rather than
	// applying one the tree would not (#2775).
	projectID, _ := stateDirProjectID(e.StateDir)
	return hubcore.SessionArchived(decisions, e.Meta.ID, projectID, "", hubcore.OrderUpdatedAt(e.Meta.UpdatedAt, e.Meta.CreatedAt), now)
}

// searchInSessions is the In sessions group (S14): the sessions whose messages
// match query and scope admits, live ones first in the Live order, then ended
// ones newest first, at most searchInSessionsLimit, each with its newest hits
// and their snippets. Without a message index there is no group.
func searchInSessions(ctx context.Context, cfg hubcore.WebConfig, query, scope string, live []appwire.SearchResult, decisions map[hubcore.ArchiveKey]bool, now time.Time) ([]appwire.SearchResult, error) {
	if cfg.MessageSearch == nil {
		return nil, nil
	}
	matches, err := cfg.MessageSearch.Match(ctx, query, searchHitsPerSession)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	var chosen []appwire.SearchResult
	seen := map[string]bool{}
	take := func(result appwire.SearchResult, isLive bool) {
		if searchScopeAdmits(scope, result.Archived, isLive) {
			result.HitCount = matches[result.ID].Count
			chosen = append(chosen, result)
		}
	}
	for _, result := range live {
		if len(chosen) == searchInSessionsLimit {
			break
		}
		if _, ok := matches[result.ID]; !ok {
			continue
		}
		seen[result.ID] = true
		take(result, true)
	}
	if cfg.Past != nil {
		for _, e := range cfg.Past.All() {
			if len(chosen) == searchInSessionsLimit {
				break
			}
			if _, ok := matches[e.Meta.ID]; !ok || seen[e.Meta.ID] {
				continue
			}
			take(pastSearchResult(e, decisions, now), false)
		}
	}
	var hits []hubcore.MessageHit
	for _, result := range chosen {
		hits = append(hits, matches[result.ID].Hits...)
	}
	texts, err := cfg.MessageSearch.Texts(ctx, hits)
	if err != nil {
		return nil, err
	}
	tokens := hubcore.SearchTokens(query)
	next := 0
	for i := range chosen {
		for _, hit := range matches[chosen[i].ID].Hits {
			chosen[i].Hits = append(chosen[i].Hits, appwire.SearchHit{
				TranscriptKey: hit.TranscriptKey,
				Position:      hit.Position,
				Snippet:       searchSnippet(texts[next], tokens),
			})
			next++
		}
	}
	return chosen, nil
}

func sortLiveForSearch(live []hubcore.LiveEntry, past *hubcore.PastIndex) {
	sort.SliceStable(live, func(i, j int) bool {
		return hubcore.LiveEntryWithPastLess(live[i], live[j], past)
	})
}

func searchPastTitle(pe hubcore.PastEntry) string {
	if title := strings.TrimSpace(pe.Meta.Name); title != "" {
		return title
	}
	return hubcore.ShortID(pe.Meta.ID)
}

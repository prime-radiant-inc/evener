package hub

import (
	"context"
	"math"
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
	decisions, err := searchDecisions(cfg)
	if err != nil {
		return appwire.SearchResponse{}, err
	}
	resp := appwire.SearchResponse{Live: []appwire.SearchResult{}, Past: []appwire.SearchResult{}, Scope: scope}
	q := strings.ToLower(strings.TrimSpace(params.Query))
	// Every past match, so the scope filters before the limit cuts: the
	// newest matches are rarely the archived ones.
	var pastMatches []hubcore.PastEntry
	pastMatched := map[string]bool{}
	if cfg.Past != nil {
		pastMatches = cfg.Past.Search(q, math.MaxInt32, 0)
		for _, e := range pastMatches {
			pastMatched[e.Meta.ID] = true
		}
	}
	var live []hubcore.LiveEntry
	isLive := map[string]bool{}
	if cfg.Roster != nil {
		live = cfg.Roster.List()
		sortLiveForSearch(live, cfg.Past)
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			isLive[le.SessionID] = true
			title := liveTitle(le.SessionID, le, cfg.Past)
			// A live session's meta is in the past index too, so a prompt or
			// working-directory match there lists it here, live.
			if q != "" && !strings.Contains(strings.ToLower(le.SessionID), q) && !strings.Contains(strings.ToLower(title), q) && !pastMatched[le.SessionID] {
				continue
			}
			if result := liveSearchResult(cfg, le, title, decisions, now); searchScopeAdmits(scope, result, true) {
				resp.Live = append(resp.Live, result)
			}
		}
	}
	for _, e := range pastMatches {
		if len(resp.Past) == searchPastLimit {
			break
		}
		if isLive[e.Meta.ID] {
			continue
		}
		if result := pastSearchResult(e, decisions, now); searchScopeAdmits(scope, result, false) {
			resp.Past = append(resp.Past, result)
		}
	}
	resp.InSessions, err = searchInSessions(ctx, cfg, params.Query, scope, live, decisions, now)
	return resp, err
}

// searchDecisions is the hub's archive decisions, or none without a store.
func searchDecisions(cfg hubcore.WebConfig) (map[hubcore.ArchiveKey]bool, error) {
	if cfg.Archive == nil {
		return map[hubcore.ArchiveKey]bool{}, nil
	}
	return cfg.Archive.Decisions()
}

// searchScopeAdmits reports whether scope keeps result: Live keeps what the
// Board's Live section holds, live and not archived; Archived keeps every
// archived session.
func searchScopeAdmits(scope string, result appwire.SearchResult, live bool) bool {
	switch scope {
	case appwire.SearchScopeLive:
		return live && !result.Archived
	case appwire.SearchScopeArchived:
		return result.Archived
	default:
		return true
	}
}

func liveSearchResult(cfg hubcore.WebConfig, le hubcore.LiveEntry, title string, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	// A live session with no meta yet has no last activity to age.
	lastActivity := now
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(le.SessionID); ok {
			lastActivity = hubcore.OrderUpdatedAt(pe.Meta.UpdatedAt, pe.Meta.CreatedAt)
		}
	}
	return appwire.SearchResult{
		ID:              le.SessionID,
		Title:           title,
		State:           hubcore.NormalizeState(le.Status),
		Project:         filepath.Base(le.WorkingDir),
		Age:             "now",
		Ref:             hubRefFromTreeNodeID(le.SessionID).String(),
		AskPending:      le.PendingAsk,
		ApprovalPending: le.PendingEscalation,
		Archived:        hubcore.LiveSessionArchived(decisions, le, lastActivity, now),
	}
}

func pastSearchResult(e hubcore.PastEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	return appwire.SearchResult{
		ID:      e.Meta.ID,
		Title:   searchPastTitle(e),
		State:   "ended",
		Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
		Age:     hubcore.AgeString(e.Meta.UpdatedAt),
		Ref:     hubRefFromTreeNodeID(e.Meta.ID).String(),
		// A past entry's state directory is named by its project's ID.
		Archived: hubcore.SessionArchived(decisions, e.Meta.ID, filepath.Base(e.StateDir), "", hubcore.OrderUpdatedAt(e.Meta.UpdatedAt, e.Meta.CreatedAt), now),
	}
}

// searchInSessions is the In sessions group (S14): the sessions whose messages
// match query and scope admits, live ones first in the Live order, then ended
// ones newest first, at most searchInSessionsLimit, each with its newest hits
// and their snippets. Without a message index there is no group.
func searchInSessions(ctx context.Context, cfg hubcore.WebConfig, query, scope string, live []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) ([]appwire.SearchResult, error) {
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
		if len(chosen) < searchInSessionsLimit && searchScopeAdmits(scope, result, isLive) {
			result.HitCount = matches[result.ID].Count
			chosen = append(chosen, result)
		}
	}
	for _, le := range live {
		if _, ok := matches[le.SessionID]; !ok || le.SessionID == "" {
			continue
		}
		seen[le.SessionID] = true
		take(liveSearchResult(cfg, le, liveTitle(le.SessionID, le, cfg.Past), decisions, now), true)
	}
	if cfg.Past != nil {
		for _, e := range cfg.Past.All() {
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

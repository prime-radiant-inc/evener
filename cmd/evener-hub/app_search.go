package hub

import (
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const searchPastLimit = 20

func hubSearch(cfg hubcore.WebConfig, params appwire.SearchParams) appwire.SearchResponse {
	resp := appwire.SearchResponse{
		Live: []appwire.SearchResult{},
		Past: []appwire.SearchResult{},
	}
	q := strings.ToLower(strings.TrimSpace(params.Query))
	// emittedLiveIDs holds running sessions that are returned under Live. A past
	// index also holds a live session's meta file once it starts, so its past row
	// is suppressed to avoid listing one session as both live and ended. Only an
	// emitted row is suppressed: the past matcher searches fields the live filter
	// does not, and suppressing an unmatched row would make it undiscoverable.
	emittedLiveIDs := map[string]struct{}{}
	if cfg.Roster != nil {
		live := cfg.Roster.List()
		sortLiveForSearch(live, cfg.Past)
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			title := liveTitle(le.SessionID, le, cfg.Past)
			if q != "" && !strings.Contains(strings.ToLower(le.SessionID), q) && !strings.Contains(strings.ToLower(title), q) {
				continue
			}
			emittedLiveIDs[le.SessionID] = struct{}{}
			resp.Live = append(resp.Live, appwire.SearchResult{
				ID:              le.SessionID,
				Title:           title,
				State:           hubcore.NormalizeState(le.Status),
				Project:         filepath.Base(le.WorkingDir),
				Age:             "now",
				Ref:             hubRefFromTreeNodeID(le.SessionID).String(),
				AskPending:      le.PendingAsk,
				ApprovalPending: le.PendingEscalation,
			})
		}
	}
	if cfg.Past != nil {
		// Over-fetch by the suppressed rows so dropping them cannot leave past
		// short of its limit while matching ended sessions wait behind them.
		for _, e := range cfg.Past.Search(q, searchPastLimit+len(emittedLiveIDs), 0) {
			if len(resp.Past) == searchPastLimit {
				break
			}
			if _, live := emittedLiveIDs[e.Meta.ID]; live {
				continue
			}
			resp.Past = append(resp.Past, appwire.SearchResult{
				ID:      e.Meta.ID,
				Title:   searchPastTitle(e),
				State:   "ended",
				Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
				Age:     hubcore.AgeString(e.Meta.UpdatedAt),
				Ref:     hubRefFromTreeNodeID(e.Meta.ID).String(),
			})
		}
	}
	return resp
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

package hubcore

import (
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
)

type sessionOrderKey struct {
	updated time.Time
	created time.Time
	title   string
	id      string
}

func sessionOrderLess(a, b sessionOrderKey) bool {
	au := OrderUpdatedAt(a.updated, a.created)
	bu := OrderUpdatedAt(b.updated, b.created)
	if !au.Equal(bu) {
		return au.After(bu)
	}
	ac := OrderCreatedAt(a.created, a.updated)
	bc := OrderCreatedAt(b.created, b.updated)
	if !ac.Equal(bc) {
		return ac.After(bc)
	}
	if cmp := compareOrderText(a.title, b.title); cmp != 0 {
		return cmp < 0
	}
	return compareOrderText(a.id, b.id) < 0
}

func OrderUpdatedAt(updated, created time.Time) time.Time {
	if !updated.IsZero() {
		return updated
	}
	return created
}

func OrderCreatedAt(created, updated time.Time) time.Time {
	if !created.IsZero() {
		return created
	}
	return updated
}

func compareOrderText(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	af := strings.ToLower(a)
	bf := strings.ToLower(b)
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func sessionMetaOrderKey(m agent.SessionMeta) sessionOrderKey {
	return sessionOrderKey{
		updated: m.UpdatedAt,
		created: m.CreatedAt,
		title:   sessionMetaOrderTitle(m),
		id:      m.ID,
	}
}

func sessionMetaOrderTitle(m agent.SessionMeta) string {
	return nodeTitle(m, nodeKind(m))
}

func sessionMetaLess(a, b agent.SessionMeta) bool {
	return sessionOrderLess(sessionMetaOrderKey(a), sessionMetaOrderKey(b))
}

func AppwireThreadLess(a, b appwire.Thread) bool {
	return sessionOrderLess(appwireThreadOrderKey(a), appwireThreadOrderKey(b))
}

func appwireThreadOrderKey(thread appwire.Thread) sessionOrderKey {
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = thread.SessionID
	}
	return sessionOrderKey{
		updated: UnixTime(thread.UpdatedAt),
		created: UnixTime(thread.CreatedAt),
		title:   title,
		id:      strutil.FirstNonEmpty(thread.ID, thread.SessionID),
	}
}

func UnixTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func UnixSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func liveEntryOrderKey(le LiveEntry, past *PastIndex) sessionOrderKey {
	if past != nil && le.SessionID != "" {
		if entry, ok := past.Find(le.SessionID); ok {
			return sessionMetaOrderKey(entry.Meta)
		}
	}
	return liveEntryFallbackOrderKey(le)
}

func liveEntryFallbackOrderKey(le LiveEntry) sessionOrderKey {
	id := strutil.FirstNonEmpty(le.SessionID, le.ThreadID, le.Address)
	return sessionOrderKey{
		updated: le.StartedAt,
		created: le.StartedAt,
		title:   id,
		id:      id,
	}
}

func liveEntryLess(a, b LiveEntry) bool {
	return sessionOrderLess(liveEntryFallbackOrderKey(a), liveEntryFallbackOrderKey(b))
}

func LiveEntryWithPastLess(a, b LiveEntry, past *PastIndex) bool {
	return sessionOrderLess(liveEntryOrderKey(a, past), liveEntryOrderKey(b, past))
}

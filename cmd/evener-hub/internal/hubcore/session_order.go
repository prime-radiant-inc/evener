package hubcore

import (
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
)

// SessionOrderKey is a session's position in the rail's order: newest first by
// update (or creation) time, then creation time, then title and ID. Title
// and ID compare trimmed and case-folded, and the raw text breaks a folded
// tie, so the order is total.
type SessionOrderKey struct {
	Updated time.Time
	Created time.Time
	Title   string
	ID      string
}

// SessionOrderLess reports whether a sorts before b in the rail's order.
func SessionOrderLess(a, b SessionOrderKey) bool {
	au := OrderUpdatedAt(a.Updated, a.Created)
	bu := OrderUpdatedAt(b.Updated, b.Created)
	if !au.Equal(bu) {
		return au.After(bu)
	}
	ac := OrderCreatedAt(a.Created, a.Updated)
	bc := OrderCreatedAt(b.Created, b.Updated)
	if !ac.Equal(bc) {
		return ac.After(bc)
	}
	if cmp := compareOrderText(a.Title, b.Title); cmp != 0 {
		return cmp < 0
	}
	return compareOrderText(a.ID, b.ID) < 0
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

func sessionMetaOrderKey(m schema.SessionMeta) SessionOrderKey {
	return SessionOrderKey{
		Updated: m.UpdatedAt,
		Created: m.CreatedAt,
		Title:   sessionMetaOrderTitle(m),
		ID:      m.ID,
	}
}

func sessionMetaOrderTitle(m schema.SessionMeta) string {
	return nodeTitle(m, nodeKind(m))
}

func sessionMetaLess(a, b schema.SessionMeta) bool {
	return SessionOrderLess(sessionMetaOrderKey(a), sessionMetaOrderKey(b))
}

// sortSessionMetas orders metas by sessionMetaLess, building each meta's order
// key once because building it allocates a truncated title.
func sortSessionMetas(metas []schema.SessionMeta) {
	if len(metas) < 2 {
		return
	}
	keys := make([]SessionOrderKey, len(metas))
	for i, m := range metas {
		keys[i] = sessionMetaOrderKey(m)
	}
	sort.Stable(keyedSessionMetas{metas: metas, keys: keys})
}

// keyedSessionMetas sorts metas and their precomputed order keys in step.
type keyedSessionMetas struct {
	metas []schema.SessionMeta
	keys  []SessionOrderKey
}

func (k keyedSessionMetas) Len() int           { return len(k.metas) }
func (k keyedSessionMetas) Less(i, j int) bool { return SessionOrderLess(k.keys[i], k.keys[j]) }
func (k keyedSessionMetas) Swap(i, j int) {
	k.metas[i], k.metas[j] = k.metas[j], k.metas[i]
	k.keys[i], k.keys[j] = k.keys[j], k.keys[i]
}

func AppwireThreadLess(a, b appwire.Thread) bool {
	return SessionOrderLess(appwireThreadOrderKey(a), appwireThreadOrderKey(b))
}

func appwireThreadOrderKey(thread appwire.Thread) SessionOrderKey {
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = thread.SessionID
	}
	return SessionOrderKey{
		Updated: UnixTime(thread.UpdatedAt),
		Created: UnixTime(thread.CreatedAt),
		Title:   title,
		ID:      envvars.FirstNonEmpty(thread.ID, thread.SessionID),
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

// UnixMilliTime is UnixTime for a Unix-millisecond wire value, such as a
// daemon's lastTurnEndedAt.
func UnixMilliTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// UnixMilliseconds is UnixSeconds in milliseconds.
func UnixMilliseconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func liveEntryOrderKey(le LiveEntry, past *PastIndex) SessionOrderKey {
	if past != nil && le.SessionID != "" {
		if entry, ok := past.Find(le.SessionID); ok {
			return sessionMetaOrderKey(entry.Meta)
		}
	}
	return liveEntryFallbackOrderKey(le)
}

func liveEntryFallbackOrderKey(le LiveEntry) SessionOrderKey {
	id := envvars.FirstNonEmpty(le.SessionID, le.ThreadID, le.Address)
	return SessionOrderKey{
		Updated: le.StartedAt,
		Created: le.StartedAt,
		Title:   id,
		ID:      id,
	}
}

func liveEntryLess(a, b LiveEntry) bool {
	return SessionOrderLess(liveEntryFallbackOrderKey(a), liveEntryFallbackOrderKey(b))
}

func LiveEntryWithPastLess(a, b LiveEntry, past *PastIndex) bool {
	return SessionOrderLess(liveEntryOrderKey(a, past), liveEntryOrderKey(b, past))
}

// TreeNodeOrderKey is a tree row's position in the rail's order: the key the
// tree sorts its metas by (sessionMetaOrderKey), read from the row.
func TreeNodeOrderKey(n TreeNode) SessionOrderKey {
	return SessionOrderKey{Updated: n.UpdatedAt, Created: n.CreatedAt, Title: n.Title, ID: n.ID}
}

// TreeNodeLess reports whether tree row a sorts before b in the rail's order.
func TreeNodeLess(a, b TreeNode) bool {
	return SessionOrderLess(TreeNodeOrderKey(a), TreeNodeOrderKey(b))
}

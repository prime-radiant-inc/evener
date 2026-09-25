package appoverlay

import (
	"encoding/json"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

// Each thread's notice ring keeps at most this many round_timings notices,
// this many of every other kind, and this many encoded bytes in total.
const (
	maxRoundTimingsNotices = 50
	maxOtherNotices        = 50
	maxNoticeBytes         = 64 << 10
)

// notice is one ring entry. Its payload lives on its charge, where the
// budget can drop it (see Budget); key, bytes and roundTimings are the ring's
// own bookkeeping under the Overlay's mutex.
type notice struct {
	key          string
	bytes        int
	roundTimings bool
	charge       *charge
}

func (n *notice) isRoundTimings() bool { return n.roundTimings }

// noticeRing is one thread's ephemeral notices, oldest first. Its methods
// run under the owning Overlay's mutex.
type noticeRing struct {
	notices      []*notice
	bytes        int
	roundTimings int
}

// add keeps n as the newest notice and evicts the oldest past the ring's
// caps. Evictions emit nothing: clients keep what they got, and a later read
// omits them. n is within every cap on its own, so it is never the one
// evicted: each cap evicts oldest first and stops at or before n.
func (r *noticeRing) add(n *notice, budget *Budget) {
	r.notices = append(r.notices, n)
	r.bytes += n.bytes
	if n.roundTimings {
		r.roundTimings++
	}
	r.evictOldest(budget, func() bool { return r.roundTimings > maxRoundTimingsNotices }, (*notice).isRoundTimings)
	r.evictOldest(budget, func() bool { return len(r.notices)-r.roundTimings > maxOtherNotices }, func(n *notice) bool { return !n.roundTimings })
	r.evictOldest(budget, func() bool { return r.bytes > maxNoticeBytes }, func(*notice) bool { return true })
}

// evictOldest removes the oldest notices matching kind while over reports
// the ring is past a cap.
func (r *noticeRing) evictOldest(budget *Budget, over func() bool, kind func(*notice) bool) {
	for i := 0; over() && i < len(r.notices); {
		if !kind(r.notices[i]) {
			i++
			continue
		}
		budget.release(r.notices[i].charge)
		r.forget(i)
	}
}

// dropEvicted forgets the notices the daemon-wide budget evicted. It takes
// the Budget's mutex under the Overlay's, the documented lock order.
func (r *noticeRing) dropEvicted(budget *Budget) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	r.dropEvictedLocked()
}

func (r *noticeRing) dropEvictedLocked() {
	for i := 0; i < len(r.notices); {
		if r.notices[i].charge.item == nil {
			r.forget(i)
			continue
		}
		i++
	}
}

// items is copies of the ring's notices, oldest first.
func (r *noticeRing) items(budget *Budget) []appwire.OverlayItem {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	r.dropEvictedLocked()
	items := make([]appwire.OverlayItem, 0, len(r.notices))
	for _, n := range r.notices {
		items = append(items, appwire.CloneOverlayItem(*n.charge.item))
	}
	return items
}

// releaseAll returns every notice's bytes to the budget and empties the ring.
func (r *noticeRing) releaseAll(budget *Budget) {
	for _, n := range r.notices {
		budget.release(n.charge)
	}
	*r = noticeRing{}
}

func (r *noticeRing) forget(i int) {
	n := r.notices[i]
	r.bytes -= n.bytes
	if n.roundTimings {
		r.roundTimings--
	}
	r.notices = append(r.notices[:i], r.notices[i+1:]...)
}

func (r *noticeRing) contains(key string) bool {
	for _, n := range r.notices {
		if n.key == key {
			return true
		}
	}
	return false
}

// encodedSize is what a notice costs its ring and the budget: its wire
// encoding's length.
func encodedSize(item appwire.OverlayItem) int {
	encoded, err := json.Marshal(item)
	if err != nil {
		// An OverlayItem is plain data; only a Raw that is not valid JSON
		// fails, and it can never be sent either. Charge it the cap so the
		// ring evicts it at once.
		return maxNoticeBytes + 1
	}
	return len(encoded)
}

// noticeAnnouncement is the notice an event shows as, or false for an event
// that is not an ephemeral notice.
func noticeAnnouncement(ev events.SessionEvent) (apptranscript.NoticeAnnouncement, bool) {
	switch data := ev.Data.(type) {
	case events.RoundTimings:
		return apptranscript.RoundTimingsAnnouncement(data), true
	case events.PromptLoadedData:
		return apptranscript.PromptLoadedAnnouncement(data), true
	case events.PluginLoadedData:
		return apptranscript.PluginLoadedAnnouncement(data), true
	case events.LoopDetectionData:
		return apptranscript.LoopDetectionAnnouncement(data), true
	case events.ContextCompactionData:
		return apptranscript.ContextCompactionAnnouncement(data), true
	case events.ForkSummaryData:
		return apptranscript.ForkSummaryAnnouncement(data), true
	case events.WarningData:
		return warningAnnouncement(data.Source, data.Title, data.Hint, data.Message), true
	case events.ErrorData:
		// A recorded error is a TURN_FAILURE entry history already shows.
		if data.Recorded {
			return apptranscript.NoticeAnnouncement{}, false
		}
		message := strings.TrimSpace(data.Error)
		if message == "" {
			message = "session error"
		}
		// A user-cancelled turn is not a failure: it shows as a warning, as
		// the live projector has always shown it.
		if apptranscript.IsCancellation(message) {
			return warningAnnouncement(data.Source, data.Title, data.Hint, message), true
		}
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, message)
		return apptranscript.NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindError, Description: info.Title, Text: message}, true
	default:
		return apptranscript.NoticeAnnouncement{}, false
	}
}

// interruptedAnnouncement is the notice a round collapses into when it ended
// with streamed content or running tools that were never recorded.
func interruptedAnnouncement() apptranscript.NoticeAnnouncement {
	return apptranscript.NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindInterrupted,
		Description: "Interrupted",
		Text:        "The model round ended before its output was recorded.",
	}
}

// warningAnnouncement shows a warning's title and message, and carries its
// source, title and hint on Raw under "warning", the fields the warning
// notification has always given clients.
func warningAnnouncement(source, title, hint, message string) apptranscript.NoticeAnnouncement {
	info := diagnostic.FromFields(source, title, hint, message)
	announcement := apptranscript.NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindWarning, Description: info.Title, Text: message}
	raw, err := json.Marshal(map[string]map[string]string{"warning": {
		"source": string(info.Source),
		"title":  info.Title,
		"hint":   info.Hint,
	}})
	if err == nil {
		announcement.Raw = raw
	}
	return announcement
}

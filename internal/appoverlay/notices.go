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

type notice struct {
	item   appwire.OverlayItem
	bytes  int
	charge *charge
}

func (n *notice) roundTimings() bool {
	return n.item.Item.EventKind == appwire.ThreadItemEventKindRoundTimings
}

// noticeRing is one thread's ephemeral notices, oldest first. Its methods
// run under the owning Overlay's mutex.
type noticeRing struct {
	notices      []*notice
	bytes        int
	roundTimings int
}

// add keeps n as the newest notice and evicts the oldest past the ring's
// caps. Evictions emit nothing: clients keep what they got, and a later read
// omits them.
func (r *noticeRing) add(n *notice, budget *Budget) {
	r.notices = append(r.notices, n)
	r.bytes += n.bytes
	if n.roundTimings() {
		r.roundTimings++
	}
	r.evictOldest(budget, func() bool { return r.roundTimings > maxRoundTimingsNotices }, (*notice).roundTimings)
	r.evictOldest(budget, func() bool { return len(r.notices)-r.roundTimings > maxOtherNotices }, func(n *notice) bool { return !n.roundTimings() })
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
	for i := 0; i < len(r.notices); {
		if r.notices[i].charge.evicted {
			r.forget(i)
			continue
		}
		i++
	}
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
	if n.roundTimings() {
		r.roundTimings--
	}
	r.notices = append(r.notices[:i], r.notices[i+1:]...)
}

func (r *noticeRing) contains(key string) bool {
	for _, n := range r.notices {
		if n.item.Key == key {
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
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Message)
		return apptranscript.NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindWarning, Description: info.Title, Text: data.Message}, true
	case events.ErrorData:
		// A recorded error is a TURN_FAILURE entry history already shows.
		if data.Recorded {
			return apptranscript.NoticeAnnouncement{}, false
		}
		message := strings.TrimSpace(data.Error)
		if message == "" {
			message = "session error"
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

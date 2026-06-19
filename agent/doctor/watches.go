package doctor

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// WatchReport is the forensic watch/delivery view of one session's jobs.jsonl.
type WatchReport struct {
	SessionID string      `json:"session_id"`
	JobsPath  string      `json:"jobs_path"`
	Watches   []WatchView `json:"watches"`
}

// WatchView is one watch's registration, delivery accounting, and self-loop
// verdict.
type WatchView struct {
	WatchID          string `json:"watch_id"`
	Generation       string `json:"generation,omitempty"`
	OwnerSessionID   string `json:"owner_session_id,omitempty"`
	VisibleSessionID string `json:"visible_session_id,omitempty"`
	Target           string `json:"target,omitempty"`
	SendTo           string `json:"send_to,omitempty"`
	Condition        string `json:"condition,omitempty"`
	Active           bool   `json:"active"`
	EndReason        string `json:"end_reason,omitempty"`

	// Delivery accounting. PendingLines is the raw watch_send_pending event count
	// — the number that overcounts deliveries when read by grep. DistinctDeliveries
	// is the settled count after coalescing collapses; Coalesced is true when the
	// two differ (the expected, non-anomalous case).
	PendingLines       int  `json:"pending_lines"`
	DistinctDeliveries int  `json:"distinct_deliveries"`
	Delivered          int  `json:"delivered"`
	Dropped            int  `json:"dropped"`
	Evicted            int  `json:"evicted"`
	StillPending       int  `json:"still_pending"`
	Coalesced          bool `json:"coalesced"`

	Deliveries []DeliveryView  `json:"deliveries,omitempty"`
	SelfLoop   SelfLoopVerdict `json:"self_loop"`
}

// DeliveryView is one settled watch-send delivery.
type DeliveryView struct {
	DeliveryID       string             `json:"delivery_id,omitempty"`
	Terminal         string             `json:"terminal"` // delivered | dropped | evicted
	TriggerIdentity  string             `json:"trigger_identity,omitempty"`
	TriggerReason    string             `json:"trigger_reason,omitempty"`
	CoalescedCount   int                `json:"coalesced_count,omitempty"`
	DiagnosticReason string             `json:"diagnostic_reason,omitempty"`
	SelfLoop         bool               `json:"self_loop"`
	Provenance       *provenance.Causal `json:"provenance,omitempty"`
}

// SelfLoopVerdict reports whether any settled delivery of the watch was caused
// (transitively) by a PRIOR delivery of the same watch. The verdict is read from
// the diagnostic Chain — a same-watch_id hop with a different delivery_id — never
// from the always-present WatchKeys stamp (which a recorded delivery carries for
// its own watch by construction, so ContainsWatch is vacuously true on it).
type SelfLoopVerdict struct {
	Detected       bool     `json:"detected"`
	DeliveryIDs    []string `json:"delivery_ids,omitempty"`
	ChainTruncated bool     `json:"chain_truncated,omitempty"` // a flagged delivery had a truncated chain — a positive signal, not a completeness guarantee
}

// WatchOpts narrows a watch report.
type WatchOpts struct {
	WatchID       string // scope to one watch_id
	SelfLoopsOnly bool   // only watches with a self-loop verdict
}

// settledDelivery is one terminal watch-send event (delivered/dropped/evicted).
type settledDelivery struct {
	ev   jobstore.Event
	kind string
}

// Watches resolves the selector and reports each watch's registration, distinct
// (coalescing-collapsed) deliveries, provenance, and self-loop verdict — read
// from the real jobstore folds + a raw scan of the settled watch_send_* events
// (FoldWatchSends discards terminal payloads, surfacing only pending frames).
func Watches(stateBase, selector string, opts WatchOpts) (WatchReport, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return WatchReport{}, err
	}
	events, err := jobstore.ReadEvents(paths.JobsPath)
	if err != nil {
		return WatchReport{}, err
	}
	return buildWatchReport(paths, events, opts), nil
}

func buildWatchReport(paths Paths, events []jobstore.Event, opts WatchOpts) WatchReport {
	registry := jobstore.FoldWatches(events)
	pending := jobstore.FoldWatchSends(events).Pending

	terminals := map[string]map[string]settledDelivery{} // watchID -> deliveryID -> settled
	pendingLines := map[string]int{}
	stillPending := map[string]int{}

	for key := range pending {
		stillPending[key.WatchID]++
	}
	for _, e := range events {
		if e.WatchSend == nil {
			continue
		}
		wID := e.WatchSend.Key.WatchID
		switch e.Kind {
		case jobstore.EventWatchSendPending:
			pendingLines[wID]++
		case jobstore.EventWatchSendDelivered, jobstore.EventWatchSendDropped, jobstore.EventWatchSendEvicted:
			byID := terminals[wID]
			if byID == nil {
				byID = map[string]settledDelivery{}
				terminals[wID] = byID
			}
			id := e.WatchSend.DeliveryID
			if prev, ok := byID[id]; !ok || e.Seq > prev.ev.Seq {
				byID[id] = settledDelivery{ev: e, kind: terminalKind(e.Kind)}
			}
		}
	}

	report := WatchReport{SessionID: paths.SessionID, JobsPath: paths.JobsPath, Watches: []WatchView{}}
	for _, wID := range orderedWatchIDs(registry, terminals) {
		if opts.WatchID != "" && wID != opts.WatchID {
			continue
		}
		view := buildWatchView(wID, registry[wID], terminals[wID], pendingLines[wID], stillPending[wID])
		if opts.SelfLoopsOnly && !view.SelfLoop.Detected {
			continue
		}
		report.Watches = append(report.Watches, view)
	}
	return report
}

func buildWatchView(wID string, rec *jobstore.WatchRecord, settledByID map[string]settledDelivery, pendingLines, stillPending int) WatchView {
	v := WatchView{WatchID: wID, PendingLines: pendingLines, StillPending: stillPending}
	if rec != nil {
		v.Generation = rec.Generation
		v.OwnerSessionID = rec.OwnerSessionID
		v.VisibleSessionID = rec.VisibleSessionID
		v.Target = rec.Target
		v.SendTo = rec.SendTo
		v.Condition = rec.Condition
		v.Active = rec.Active
		v.EndReason = rec.EndReason
	}

	deliveries := make([]settledDelivery, 0, len(settledByID))
	for _, d := range settledByID {
		deliveries = append(deliveries, d)
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].ev.Seq < deliveries[j].ev.Seq })

	for _, d := range deliveries {
		ws := d.ev.WatchSend
		dv := DeliveryView{
			DeliveryID:       ws.DeliveryID,
			Terminal:         d.kind,
			TriggerIdentity:  ws.TriggerIdentity,
			TriggerReason:    ws.TriggerReason,
			CoalescedCount:   ws.CoalescedCount,
			DiagnosticReason: ws.DiagnosticReason,
			Provenance:       ws.Provenance,
			SelfLoop:         deliverySelfLoop(wID, ws),
		}
		switch d.kind {
		case "delivered":
			v.Delivered++
		case "dropped":
			v.Dropped++
		case "evicted":
			v.Evicted++
		}
		if dv.SelfLoop {
			v.SelfLoop.Detected = true
			v.SelfLoop.DeliveryIDs = append(v.SelfLoop.DeliveryIDs, ws.DeliveryID)
			if ws.Provenance != nil && ws.Provenance.ChainTruncated {
				v.SelfLoop.ChainTruncated = true
			}
		}
		v.Deliveries = append(v.Deliveries, dv)
	}
	v.DistinctDeliveries = len(deliveries)
	v.Coalesced = v.PendingLines > v.DistinctDeliveries
	return v
}

// deliverySelfLoop reports whether a settled delivery was caused by a prior
// delivery of the SAME watch — a same-watch_id hop in the diagnostic Chain whose
// delivery_id differs from this delivery's own (delivery-time) stamp. The own
// stamp (matching delivery_id) is excluded; the WatchKeys set is never consulted
// (it always contains the watch's own key on a recorded delivery).
func deliverySelfLoop(watchID string, ws *jobstore.WatchSendState) bool {
	if ws == nil || ws.Provenance == nil {
		return false
	}
	sameWatch := 0
	for _, hop := range ws.Provenance.Chain {
		if hop.WatchID != watchID {
			continue
		}
		sameWatch++
		if ws.DeliveryID != "" && hop.DeliveryID != ws.DeliveryID {
			return true
		}
	}
	// Fallback only when the delivery id is unavailable to exclude the own stamp.
	if ws.DeliveryID == "" {
		return sameWatch >= 2
	}
	return false
}

func terminalKind(k jobstore.EventKind) string {
	switch k {
	case jobstore.EventWatchSendDelivered:
		return "delivered"
	case jobstore.EventWatchSendDropped:
		return "dropped"
	case jobstore.EventWatchSendEvicted:
		return "evicted"
	}
	return string(k)
}

func orderedWatchIDs(registry map[string]*jobstore.WatchRecord, terminals map[string]map[string]settledDelivery) []string {
	seen := map[string]bool{}
	ids := make([]string, 0, len(registry)+len(terminals))
	for id := range registry {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range terminals {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	return ids
}

// RenderWatches renders a WatchReport as a human-readable summary (the default,
// non-JSON output). Summary-by-default: headers, distinct counts, and verdicts.
func RenderWatches(r WatchReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s  (jobs: %s)\n", r.SessionID, r.JobsPath)
	if len(r.Watches) == 0 {
		b.WriteString("no watches recorded\n")
		return b.String()
	}
	for _, w := range r.Watches {
		b.WriteString("\n")
		status := "active"
		if !w.Active {
			status = "ended"
			if w.EndReason != "" {
				status = "ended: " + w.EndReason
			}
		}
		fmt.Fprintf(&b, "watch %s", w.WatchID)
		if w.Generation != "" {
			fmt.Fprintf(&b, " [gen %s]", w.Generation)
		}
		fmt.Fprintf(&b, "  (%s)\n", status)
		if w.Target != "" || w.SendTo != "" || w.Condition != "" {
			fmt.Fprintf(&b, "  target=%s  send_to=%s  condition=%s\n", dash(w.Target), dash(w.SendTo), dash(w.Condition))
		}
		if w.OwnerSessionID != "" || w.VisibleSessionID != "" {
			fmt.Fprintf(&b, "  owner=%s  visible=%s\n", dash(w.OwnerSessionID), dash(w.VisibleSessionID))
		}
		fmt.Fprintf(&b, "  deliveries: %d distinct (%d delivered, %d dropped, %d evicted) from %d pending lines",
			w.DistinctDeliveries, w.Delivered, w.Dropped, w.Evicted, w.PendingLines)
		if w.Coalesced {
			b.WriteString("  [latest-wins coalescing collapsed — expected]")
		}
		b.WriteString("\n")
		if w.StillPending > 0 {
			fmt.Fprintf(&b, "  still-pending (unsettled) frames: %d\n", w.StillPending)
		}
		if w.SelfLoop.Detected {
			fmt.Fprintf(&b, "  SELF-LOOP: %d deliver(ies) caused by a prior delivery of this watch: %s\n",
				len(w.SelfLoop.DeliveryIDs), strings.Join(w.SelfLoop.DeliveryIDs, ", "))
			if w.SelfLoop.ChainTruncated {
				b.WriteString("    (a flagged chain was truncated — a deeper loop may have lost middle hops)\n")
			}
		} else {
			b.WriteString("  self-loop: none (verdict from the provenance Chain; the WatchKeys stamp is always present and is not the signal)\n")
		}
		for _, d := range w.Deliveries {
			fmt.Fprintf(&b, "  · %s %s", dash(d.DeliveryID), d.Terminal)
			if d.TriggerIdentity != "" || d.TriggerReason != "" {
				fmt.Fprintf(&b, "  trigger=%s/%s", dash(d.TriggerIdentity), dash(d.TriggerReason))
			}
			if d.CoalescedCount > 0 {
				fmt.Fprintf(&b, "  coalesced=%d", d.CoalescedCount)
			}
			if d.DiagnosticReason != "" {
				fmt.Fprintf(&b, "  reason=%s", d.DiagnosticReason)
			}
			if d.SelfLoop {
				b.WriteString("  [SELF-LOOP]")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
)

// liveSteerWaitIgnoredReason returns a note when a caller passed a positive
// max_wait_ms but the send was a live steer of a running delegate, which returns on
// delivery and cannot honor the wait. It returns "" when the wait was honored (a
// resumed job) or not requested, so the field stays omitted in the common case.
func liveSteerWaitIgnoredReason(blockTimeoutMS int, status jobstore.Status, action string) string {
	if blockTimeoutMS > 0 && (status == jobstore.StatusRunning && action == "steered" || action == "delivered") {
		return "live steer returns on delivery; max_wait_ms applies only to started jobs"
	}
	return ""
}

func jobWatchToolWithContext(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	a, err := watchArgsFromToolArgs(args)
	if err != nil {
		return "", err
	}
	a.ResultTool = s.resultToolName()
	if a.Operation == "create" && watchArgsIsTimer(a) && s.cfg.TurnEndsProcess {
		return "", errors.New("invalid_request: timers need a session that outlives the turn")
	}
	var res watchResult
	switch a.Operation {
	case "create":
		var source watchSource
		source, err = normalizeWatchSource(a.Source)
		if err != nil {
			return "", err
		}
		if source.Kind == watchSourceParentSession {
			if s.delegateController == nil {
				return "", errors.New("source_not_watchable: stable delegate controller is unavailable")
			}
			actor, actorErr := s.delegateActor(ctx)
			if actorErr != nil {
				return "", actorErr
			}
			binding, bindErr := s.delegateController.ResolveParentWatchSource(actor)
			if bindErr != nil {
				return "", bindErr
			}
			res, err = s.configureStableWatchOnSource("parent", binding, a)
			break
		}
		if source.Kind == watchSourceDelegate {
			if s.delegateController == nil {
				return "", errors.New("delegate controller is unavailable")
			}
			actor, actorErr := s.delegateActor(ctx)
			if actorErr != nil {
				return "", actorErr
			}
			binding, bindErr := s.delegateController.ResolveStableWatchSource(actor, source.Public)
			if bindErr != nil {
				return "", bindErr
			}
			res, err = s.configureStableWatchOnSource(source.Public, binding, a)
			break
		}
		a.Source = source.Public
		a.Target = source.Internal
		if source.Kind == watchSourceConcreteJob {
			if ownerRes, forwarded, ownerErr := s.configureDescendantReceiverWatch(a); forwarded || ownerErr != nil {
				res, err = ownerRes, ownerErr
				break
			}
		}
		res, err = jm.configureWatch(a)
	case "clear":
		local, localErr := jm.hasWatchID(a.WatchID)
		if localErr != nil {
			return "", localErr
		}
		if local {
			res, err = jm.clearWatchByID(a.WatchID)
			break
		}
		if ownerRes, found, ownerErr := s.clearStableReceiverWatchByID(a.WatchID); found || ownerErr != nil {
			res, err = ownerRes, ownerErr
			break
		}
		// #655: the watch may still live in this manager keyed to another
		// session's receiver (hasWatchID reports the visibility verdict, not
		// presence), or in a descendant's manager the receiver-path above did
		// not cover. Either way the clear is authorized by topology — the
		// receiver must be this session's own delegate or a descendant of it —
		// never by source labels: a source delegate must not be able to clear
		// its ancestor's watch on it, and a sibling must not clear another
		// sibling's.
		if receiverSession, receiverDelegate, ok := s.receiverWatchAnywhereByID(a.WatchID); ok {
			if s.delegateController == nil || !s.delegateController.watchClearAuthority(s.ID(), s.owningDelegateID, receiverSession, receiverDelegate) {
				receiver := receiverDelegate
				if receiver == "" {
					receiver = "session " + receiverSession
				}
				return "", fmt.Errorf("invalid_request: watch %s delivers to %s, which this session may not clear", a.WatchID, receiver)
			}
			holder := s.jobManagerHoldingWatch(a.WatchID)
			if holder == nil {
				holder = jm
			}
			res, err = holder.clearReceiverWatchByID(a.WatchID, receiverSession, receiverDelegate)
			break
		}
		res, err = jm.clearWatchByID(a.WatchID)
	case "list":
		return marshalWatchListResult(s.watchListToolResultWithDescendantReceivers(jm.watchListToolResult()), maxChars)
	case "inspect":
		inspect := jm.inspectWatchByID(a.WatchID)
		if watchInspectFound(inspect) {
			return marshalWatchInspectResult(inspect, maxChars)
		}
		if ownerInspect, found := s.inspectDescendantReceiverWatchByID(a.WatchID); found {
			return marshalWatchInspectResult(ownerInspect, maxChars)
		}
		return marshalWatchInspectResult(inspect, maxChars)
	}
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}

func (s *Session) configureStableWatchOnSource(sourcePublic string, binding delegateWatchSourceBinding, a watchArgs) (watchResult, error) {
	if s == nil || binding.runtime == nil || binding.runtime.jobManager == nil || s.delegateController == nil {
		return watchResult{}, errors.New("source_not_watchable: stable watch source is unavailable")
	}
	a.Source = sourcePublic
	a.Target = runtimeMessageAliasCaller
	a.ReceiverSessionID = s.ID()
	a.ReceiverDelegateID = s.owningDelegateID
	a.StableReceiver = true
	a.ReceiverSendInternal = true
	if binding.lease != nil {
		a.SourceDelegateID = binding.lease.delegateID
		a.SourceGeneration = binding.lease.generation
	}
	return binding.runtime.jobManager.configureWatch(a)
}

func (s *Session) configureDescendantReceiverWatch(a watchArgs) (watchResult, bool, error) {
	if s == nil || !strings.HasPrefix(a.Target, "job_") {
		return watchResult{}, false, nil
	}
	ownerJM, ownerSess, _, _, ok := s.resolveDescendantJobOwner(a.Target)
	if !ok || ownerJM == nil || ownerSess == nil {
		return watchResult{}, false, nil
	}
	childArgs := a
	childArgs.Source = a.Source
	childArgs.Target = a.Target
	childArgs.ReceiverSessionID = s.ID()
	childArgs.ReceiverDelegateID = ""
	childArgs.ReceiverNotify = s.enqueueJobNotificationAndNotify
	childArgs.ReceiverHoldWake = s.holdJobNotificationWake
	res, err := ownerJM.configureWatch(childArgs)
	return res, true, err
}

func (s *Session) watchListToolResultWithDescendantReceivers(local jobWatchListToolResult) jobWatchListToolResult {
	for _, child := range s.watchHolderSessions() {
		descendant := child.jobManager.watchListToolResultForReceiverShapes(s.ID(), s.owningDelegateID)
		local.Watches = append(local.Watches, descendant.Watches...)
		local.RecentWatches = append(local.RecentWatches, descendant.RecentWatches...)
	}
	// local already carries this session's own manager's rows, and a holder can
	// share that manager, so the merge can duplicate a watch. Collapse by
	// WatchID (a watch has one ID across every projection).
	local.Watches = dedupeWatchRowsByID(local.Watches)
	sort.SliceStable(local.Watches, func(i, j int) bool {
		if local.Watches[i].Source != local.Watches[j].Source {
			return local.Watches[i].Source < local.Watches[j].Source
		}
		return local.Watches[i].WatchID < local.Watches[j].WatchID
	})
	// The recent-watch rows come from several managers, each with its own
	// latest-first, capped ring; merge them into one latest-first, capped list so
	// the aggregate keeps the single-manager contract.
	local.RecentWatches = dedupeWatchRowsByID(local.RecentWatches)
	sortWatchHistoryLatestFirst(local.RecentWatches)
	if len(local.RecentWatches) > watchHistoryCap {
		local.RecentWatches = local.RecentWatches[:watchHistoryCap]
	}
	local.Count = len(local.Watches)
	return local
}

// dedupeWatchRowsByID removes later rows sharing a non-empty WatchID with an
// earlier row, preserving the first occurrence's position. Rows with an empty
// WatchID are never treated as duplicates.
func dedupeWatchRowsByID(rows []jobWatchInspectToolResult) []jobWatchInspectToolResult {
	seen := make(map[string]struct{}, len(rows))
	out := make([]jobWatchInspectToolResult, 0, len(rows))
	for _, row := range rows {
		if row.WatchID != "" {
			if _, dup := seen[row.WatchID]; dup {
				continue
			}
			seen[row.WatchID] = struct{}{}
		}
		out = append(out, row)
	}
	return out
}

// sortWatchHistoryLatestFirst orders recent-watch rows by EndedAt descending,
// with WatchID as a deterministic tie-breaker. A row whose EndedAt does not
// parse sorts after every parseable one.
func sortWatchHistoryLatestFirst(rows []jobWatchInspectToolResult) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, rows[i].EndedAt)
		right, rightErr := time.Parse(time.RFC3339Nano, rows[j].EndedAt)
		switch {
		case leftErr == nil && rightErr == nil && !left.Equal(right):
			return left.After(right)
		case leftErr == nil && rightErr != nil:
			return true
		case leftErr != nil && rightErr == nil:
			return false
		}
		return rows[i].WatchID < rows[j].WatchID
	})
}

func (s *Session) inspectDescendantReceiverWatchByID(watchID string) (jobWatchInspectToolResult, bool) {
	for _, child := range s.watchHolderSessions() {
		if inspect, ok := child.jobManager.inspectReceiverWatchByIDShapes(watchID, s.ID(), s.owningDelegateID); ok {
			return inspect, true
		}
	}
	return jobWatchInspectToolResult{}, false
}

func (s *Session) clearStableReceiverWatchByID(watchID string) (watchResult, bool, error) {
	receiverDelegateID := s.owningDelegateID
	for _, child := range s.watchHolderSessions() {
		if _, ok := child.jobManager.inspectReceiverWatchByID(watchID, s.ID(), receiverDelegateID); !ok {
			continue
		}
		res, err := child.jobManager.clearReceiverWatchByID(watchID, s.ID(), receiverDelegateID)
		return res, true, err
	}
	return watchResult{}, false, nil
}

// receiverWatchAnywhereByID finds a watch's receiver identity across this
// session's own manager and the watch-source sessions, ignoring visibility.
// Returns ok=false when no manager holds the watch.
func (s *Session) receiverWatchAnywhereByID(watchID string) (receiverSessionID, receiverDelegateID string, found bool) {
	if s == nil || watchID == "" {
		return "", "", false
	}
	if s.jobManager != nil {
		if receiverSession, receiverDelegate, ok := s.jobManager.watchReceiverIdentity(watchID); ok {
			return receiverSession, receiverDelegate, true
		}
	}
	for _, holder := range s.watchHolderSessions() {
		if holder.jobManager == s.jobManager {
			continue
		}
		if receiverSession, receiverDelegate, ok := holder.jobManager.watchReceiverIdentity(watchID); ok {
			return receiverSession, receiverDelegate, true
		}
	}
	return "", "", false
}

// jobManagerHoldingWatch returns the manager (of this session or the watch
// source sessions) that currently holds the watch, or nil when none does.
func (s *Session) jobManagerHoldingWatch(watchID string) *jobManager {
	if s == nil || watchID == "" {
		return nil
	}
	holders := make([]*jobManager, 0, 2)
	if s.jobManager != nil {
		holders = append(holders, s.jobManager)
	}
	for _, holder := range s.watchHolderSessions() {
		if holder.jobManager != s.jobManager {
			holders = append(holders, holder.jobManager)
		}
	}
	for _, holder := range holders {
		if _, _, found := holder.watchReceiverIdentity(watchID); found {
			return holder
		}
	}
	return nil
}

func (s *Session) stableWatchSourceSessions() []*Session {
	if s == nil || s.delegateController == nil {
		return s.liveDescendantSessions()
	}
	return s.delegateController.watchSourceSessions()
}

// watchHolderSessions returns every session whose job manager may hold a
// receiver-keyed watch relevant to this session, deduplicated by job manager. It
// covers the stable controller sources (the root runtime plus every live
// delegate runtime) AND their live ordinary-subagent descendants:
// configureDescendantReceiverWatch routes its watch into the target job's owner
// manager, and resolveDescendantJobOwner finds that owner by walking
// liveSubagentSessions, so an ordinary nested subagent's manager is a real
// holder the stable sources alone do not name.
func (s *Session) watchHolderSessions() []*Session {
	if s == nil {
		return nil
	}
	// Collect distinct sessions first, so a session that shares a manager with a
	// sibling still contributes its own descendants to the traversal.
	sessionSeen := make(map[*Session]struct{})
	var sessions []*Session
	add := func(session *Session) {
		if session == nil {
			return
		}
		if _, ok := sessionSeen[session]; ok {
			return
		}
		sessionSeen[session] = struct{}{}
		sessions = append(sessions, session)
	}
	addTree := func(root *Session) {
		add(root)
		for _, descendant := range root.liveDescendantSessions() {
			add(descendant)
		}
	}
	for _, source := range s.stableWatchSourceSessions() {
		addTree(source)
	}
	addTree(s)
	// Dedup by manager for the scan: two live sessions can share one manager, and
	// a per-session scan would double every row.
	managerSeen := make(map[*jobManager]struct{})
	out := make([]*Session, 0, len(sessions))
	for _, session := range sessions {
		if session.jobManager == nil {
			continue
		}
		if _, ok := managerSeen[session.jobManager]; ok {
			continue
		}
		managerSeen[session.jobManager] = struct{}{}
		out = append(out, session)
	}
	return out
}

// liveWatchesDeliveringToDelegate returns the armed watches that deliver to a
// delegate and the members of the subtree rooted at it, keyed by receiver
// identity — the #655 inventory. The stopper (parent) session is always a
// member of stableWatchSourceSessions(), and the parent-source watch an
// observer child installs lives in that same manager
// (configureStableWatchOnSource), so one scan covers it without
// double-reporting. Receiver keys come from the durable descriptors, readable
// before, during, and after a stop. Managers are deduped per delegate: two
// live sessions can share one job manager, and a naive per-session append
// would double every row. Both receiver-key shapes are scanned: the
// delegate-keyed (session, delegate) pair and the session-keyed (session, "")
// pair a configureDescendantReceiverWatch installs.
//
// Boundary: the scan covers the live manager-holding set — the stable
// controller sources (root runtime plus live delegate runtimes), the stopper's
// own manager, and the live ordinary-subagent descendants of each
// (watchHolderSessions). A subtree member whose owning session is not live (a
// cold, idle descendant that no live runtime registers) is not scanned — a
// watch held only in that member's cold manager is not reported. The receiver
// key itself is durable, so watches held in any live manager (including the
// stopper's) that deliver to a cold member ARE reported.
func (s *Session) liveWatchesDeliveringToDelegate(delegateID string) []watchListEntry {
	if s == nil || delegateID == "" || s.delegateController == nil {
		return nil
	}
	receiverKeys := s.delegateController.subtreeReceiverKeysForDelegate(delegateID)
	if len(receiverKeys) == 0 {
		return nil
	}
	var entries []watchListEntry
	for _, holder := range s.watchHolderSessions() {
		for _, key := range receiverKeys {
			entries = append(entries, holder.jobManager.liveWatchSummariesForReceiver(key.sessionID, key.delegateID)...)
		}
	}
	sort.SliceStable(entries, watchListEntryLess(entries))
	return entries
}

func (s *Session) liveDescendantSessions() []*Session {
	if s == nil {
		return nil
	}
	var out []*Session
	for _, child := range s.liveSubagentSessions() {
		out = append(out, child)
		out = append(out, child.liveDescendantSessions()...)
	}
	return out
}

func watchInspectFound(inspect jobWatchInspectToolResult) bool {
	return inspect.Watching || inspect.Source != "" || inspect.EndReason != ""
}

type jobWatchToolResult struct {
	WatchID            string                   `json:"watch_id,omitempty"`
	Source             string                   `json:"source"`
	Watching           bool                     `json:"watching"`
	OutputMatch        string                   `json:"output_match,omitempty"`
	Events             []string                 `json:"events,omitempty"`
	EventFilter        *jobWatchToolEventFilter `json:"event_filter,omitempty"`
	ProgressIntervalMS int                      `json:"progress_interval_ms,omitempty"`
	AfterSeconds       int                      `json:"after_seconds,omitempty"`
	RepeatSeconds      int                      `json:"repeat_seconds,omitempty"`
	Note               string                   `json:"note,omitempty"`
	Send               *jobWatchToolSendArgs    `json:"send,omitempty"`
	// replaced_existing and fired serialize explicitly even when false: the
	// contract's install example shows replaced_existing:false, and §7.1
	// promises "fired=false on none" for terminal catch-up.
	ReplacedExisting bool   `json:"replaced_existing"`
	Fired            bool   `json:"fired"`
	TerminalCatchup  bool   `json:"terminal_catchup,omitempty"`
	Status           string `json:"status,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type jobWatchToolEventFilter struct {
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
}

type jobWatchListToolResult struct {
	Watches       []jobWatchInspectToolResult `json:"watches"`
	RecentWatches []jobWatchInspectToolResult `json:"recent_watches,omitempty"`
	Count         int                         `json:"count"`
}

type jobWatchInspectToolResult struct {
	WatchID   string `json:"watch_id"`
	Source    string `json:"source,omitempty"`
	Watching  bool   `json:"watching"`
	Condition string `json:"condition,omitempty"`
	// Note rides beside the Condition string: note text is free prose that
	// may itself contain delimiter-looking text ("; events: [...]"), which
	// the flattened Condition grammar cannot carry unambiguously. Readers prefer this field and fall back to the note: clause.
	Note       string `json:"note,omitempty"`
	Deliveries int    `json:"deliveries,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	EndReason  string `json:"end_reason,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

type jobWatchToolSendArgs struct {
	To             string `json:"to"`
	Message        string `json:"message,omitempty"`
	IncludeExcerpt bool   `json:"include_excerpt,omitempty"`
}

func marshalWatchResult(res watchResult, maxChars int) (any, error) {
	_ = maxChars
	out := jobWatchToolResult{
		WatchID:            res.WatchID,
		Source:             watchPublicSource(res.Source, res.Target),
		Watching:           res.Watching,
		OutputMatch:        res.OutputMatch,
		Events:             res.Events,
		ProgressIntervalMS: res.ProgressIntervalMS,
		Note:               res.Note,
		ReplacedExisting:   res.ReplacedExisting,
		Fired:              res.Fired,
		TerminalCatchup:    res.TerminalCatchup,
		Status:             res.Status,
		Reason:             res.Reason,
	}
	if res.OneShot {
		out.AfterSeconds = res.TimerSeconds
	} else {
		out.RepeatSeconds = res.TimerSeconds
	}
	if res.EventFilter != nil {
		out.EventFilter = &jobWatchToolEventFilter{
			ToolName: res.EventFilter.ToolName,
			Status:   res.EventFilter.Status,
		}
	}
	if res.Send != nil {
		out.Send = &jobWatchToolSendArgs{
			To:             res.Send.To,
			Message:        res.Send.Message,
			IncludeExcerpt: res.Send.IncludeExcerpt,
		}
	}
	return tool.StateResult{Output: formatJobWatch(out), State: out}, nil
}

func marshalWatchListResult(res jobWatchListToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchList(res), State: res}, nil
}

func marshalWatchInspectResult(res jobWatchInspectToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchInspect(res), State: res}, nil
}

// formatJobWatch renders a job_watch result as a one-line footer summarizing the
// watch's source, trigger condition, and disposition.
func formatJobWatch(out jobWatchToolResult) string {
	if out.TerminalCatchup {
		parts := []string{"watch on " + out.Source, "terminal catch-up"}
		if out.Fired {
			parts = append(parts, "fired")
		} else {
			parts = append(parts, "not fired")
		}
		if out.Status != "" {
			parts = append(parts, out.Status)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	if !out.Watching {
		if out.Source == "" && out.WatchID != "" {
			return fmt.Sprintf("[watch_id %s cleared]", out.WatchID)
		}
		parts := []string{"watch on " + out.Source + " cleared"}
		if out.WatchID != "" {
			parts = append(parts, "watch_id "+out.WatchID)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	parts := []string{"watching " + out.Source}
	if out.WatchID != "" {
		parts = append(parts, "watch_id "+out.WatchID)
	}
	var cond []string
	if out.OutputMatch != "" {
		cond = append(cond, "output_match: "+out.OutputMatch)
	}
	if len(out.Events) > 0 {
		events := "events: " + strings.Join(out.Events, ",")
		if filter := formatJobWatchEventFilter(out.EventFilter); filter != "" {
			events += " " + filter
		}
		cond = append(cond, events)
	}
	switch {
	case out.AfterSeconds > 0:
		cond = append(cond, fmt.Sprintf("after %ds", out.AfterSeconds))
	case out.RepeatSeconds > 0:
		cond = append(cond, fmt.Sprintf("every %ds", out.RepeatSeconds))
	case out.ProgressIntervalMS > 0:
		cond = append(cond, fmt.Sprintf("progress_interval_ms %dms", out.ProgressIntervalMS))
	}
	// The note labels any watch, not just a timer: it is the prose payload the
	// fire shows back, so it renders after whatever the watch triggers on.
	if out.Note != "" {
		cond = append(cond, "note: "+out.Note)
	}
	if len(cond) > 0 {
		parts = append(parts, strings.Join(cond, " "))
	}
	if out.Send != nil && out.Send.To != "" {
		parts = append(parts, "→ "+out.Send.To)
	}
	if out.ReplacedExisting {
		parts = append(parts, "replaced existing")
	}
	if out.Fired {
		parts = append(parts, "fired")
	}
	if out.Status != "" {
		parts = append(parts, out.Status)
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

func formatJobWatchEventFilter(filter *jobWatchToolEventFilter) string {
	if filter == nil {
		return ""
	}
	var parts []string
	if filter.ToolName != "" {
		parts = append(parts, "tool_name="+filter.ToolName)
	}
	if filter.Status != "" {
		parts = append(parts, "status="+filter.Status)
	}
	if len(parts) == 0 {
		return ""
	}
	return "where " + strings.Join(parts, ",")
}

func formatJobWatchList(out jobWatchListToolResult) string {
	if len(out.Watches) == 0 && len(out.RecentWatches) == 0 {
		return "no watches"
	}
	var b strings.Builder
	for _, w := range out.Watches {
		status := "watching"
		if !w.Watching {
			status = "pending"
		}
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, status, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, w.EndReason, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatJobWatchInspect(out jobWatchInspectToolResult) string {
	if out.Watching {
		parts := []string{"watching " + out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	if out.EndReason != "" {
		return fmt.Sprintf("%s  %s  %s", out.WatchID, out.EndReason, out.Source)
	}
	if out.Source != "" {
		parts := []string{"pending", out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	return out.WatchID + "  not found"
}

// watchIntArg reads an integer job_watch argument strictly: absent or null is
// (0, false, nil); an int or an integral, finite float64 is its value; anything
// else (a string, 1.5, NaN) is invalid_request naming the field. Providers hand
// numbers over as float64, so silently truncating would hide a model error.
// optionalWholeIntArg cannot stand in for it: that helper rejects negative
// values and native ints, and this one hands every integer to
// normalizeWatchArgs, which owns the per-field bounds.
// The only numeric bound here is what int can hold; a field with a documented
// maximum (the timer seconds) enforces it in normalizeWatchArgs instead.
func watchIntArg(args map[string]any, key string) (int, bool, error) {
	// float64 cannot represent math.MaxInt exactly - it rounds up to 2^63 - so
	// the upper bound is exclusive, which keeps every float64 that reaches the
	// int conversion below in range. math.MinInt is exactly representable and
	// stays inclusive.
	const aboveMaxInt = float64(math.MaxInt) + 1
	raw, present := args[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case int:
		return v, true, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v >= aboveMaxInt || v < math.MinInt {
			return 0, false, fmt.Errorf("invalid_request: %s must be an integer", key)
		}
		return int(v), true, nil
	default:
		return 0, false, fmt.Errorf("invalid_request: %s must be an integer", key)
	}
}

func watchArgsFromToolArgs(args map[string]any) (watchArgs, error) {
	operation := strings.TrimSpace(stringArg(args, "operation"))
	if operation == "" {
		return watchArgs{}, errors.New("invalid_request: operation is required")
	}
	if _, ok := args["target"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch uses source, not target")
	}
	if _, ok := args["send"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch delivers to the watcher automatically; send is not a public argument")
	}
	if _, ok := args["receiver_session_id"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch derives its receiver from the watcher session")
	}
	if _, ok := args["receiver_delegate_id"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch derives its receiver from the watcher session")
	}
	// A present non-string source is rejected rather than coerced: empty is not a
	// neutral source, since a create that names none and asks for a timer
	// becomes a self timer, so coercion would build a watch on something other
	// than what was asked for.
	source, err := optionalStringArg(args, "source")
	if err != nil {
		return watchArgs{}, err
	}
	a := watchArgs{
		Operation:   operation,
		WatchID:     strings.TrimSpace(stringArg(args, "watch_id")),
		Source:      strings.TrimSpace(source),
		OutputMatch: stringArg(args, "output_match"),
	}
	for _, field := range []struct {
		key string
		dst *int
	}{
		{"progress_interval_ms", &a.ProgressIntervalMS},
		{"every", &a.Every},
		{"after_seconds", &a.AfterSeconds},
		{"repeat_seconds", &a.RepeatSeconds},
	} {
		n, ok, err := watchIntArg(args, field.key)
		if err != nil {
			return watchArgs{}, err
		}
		if ok {
			*field.dst = n
		}
	}
	a.Note = stringArg(args, "note")
	events, err := stringArrayArg(args, "events")
	if err != nil {
		return watchArgs{}, err
	}
	a.Events = events
	eventFilter, err := watchEventFilterArg(args)
	if err != nil {
		return watchArgs{}, err
	}
	a.EventFilter = eventFilter
	normalizeWatchArgsForOperation(&a)
	if a.Operation == "create" && a.Source == "" && watchArgsIsTimer(a) {
		a.Source = "self"
	}
	missingWatchID := false
	switch a.Operation {
	case "create":
		if a.Source == "" {
			return watchArgs{}, errors.New("invalid_request: source is required")
		}
	case "list":
		if a.Source != "" || a.WatchID != "" {
			return watchArgs{}, errors.New("invalid_request: list requires no source or watch_id")
		}
	case "inspect", "clear":
		if a.WatchID == "" {
			missingWatchID = true
		}
	default:
		return watchArgs{}, fmt.Errorf("invalid_request: unsupported operation %q", a.Operation)
	}
	if err := rejectWatchTriggerFieldsOnNonCreate(args, a); err != nil {
		return watchArgs{}, err
	}
	if missingWatchID {
		return watchArgs{}, fmt.Errorf("invalid_request: watch_id is required for operation=%q; use %s", a.Operation, watchOperationRepairShape(a))
	}
	if a.Source == "*" {
		return watchArgs{}, errors.New("invalid_request: wildcard watch target is not supported in v1")
	}
	return a, nil
}

// normalizeWatchArgsForOperation erases only the exact neutral trigger values
// providers commonly materialize on inspection operations. The raw values stay
// in the tool call record for diagnostics; meaningful values are rejected below.
func normalizeWatchArgsForOperation(a *watchArgs) {
	if a.Operation == "create" {
		return
	}
	if len(a.Events) == 0 {
		a.Events = nil
	}
	if a.Every == 0 || a.Every == 1 {
		a.Every = 0
	}
}

// watchTriggerFieldNames lists the create-only arguments in the DefJobWatch
// property order: the fields that select what a created watch fires on, plus
// note, which is the watch's payload rather than a trigger. They are meaningful
// only for operation="create"; list/inspect/clear take only watch_id, so one of
// these beside them was previously parsed and then silently ignored.
var watchTriggerFieldNames = []string{"output_match", "progress_interval_ms", "events", "every", "event_filter", "after_seconds", "repeat_seconds", "note"}

// rejectWatchTriggerFieldsOnNonCreate returns an invalid_request naming every
// trigger field the call actually supplied alongside a non-create operation.
// Omitting all of them stays valid: create on a granted cross-session source
// (parent) watches all bounded public events, and list/inspect/clear need none.
// Null, empty, and default values count as omitted. This is deliberately
// operation-aware: create keeps its full trigger vocabulary, while list,
// inspect, and clear tolerate provider-materialized optional fields without
// accepting a real trigger outside create.
func rejectWatchTriggerFieldsOnNonCreate(args map[string]any, a watchArgs) error {
	if a.Operation == "create" {
		return nil
	}
	var supplied []string
	for _, name := range watchTriggerFieldNames {
		value, ok := args[name]
		if !ok || watchTriggerArgumentIsNeutral(name, value, a) {
			continue
		}
		supplied = append(supplied, formatWatchTriggerArgument(name, value))
	}
	if len(supplied) == 0 {
		return nil
	}
	missingWatchID := (a.Operation == "inspect" || a.Operation == "clear") && a.WatchID == ""
	missing := ""
	if missingWatchID {
		missing = fmt.Sprintf("watch_id is required for operation=%q; ", a.Operation)
	}
	return fmt.Errorf(
		"invalid_request: %strigger fields apply only to operation=\"create\"; received %s with operation=%q — set operation=\"create\" to arm a watch, or remove those fields and use %s",
		missing, strings.Join(supplied, ", "), a.Operation, watchOperationRepairShape(a),
	)
}

func watchTriggerArgumentIsNeutral(name string, value any, a watchArgs) bool {
	if value == nil {
		return true
	}
	switch name {
	case "output_match":
		_, ok := value.(string)
		return ok && a.OutputMatch == ""
	case "events":
		return len(a.Events) == 0
	case "event_filter":
		return a.EventFilter == nil
	case "progress_interval_ms":
		return a.ProgressIntervalMS == 0 && watchIntegerValue(value) == 0
	case "every":
		return (a.Every == 0 || a.Every == 1) && (watchIntegerValue(value) == 0 || watchIntegerValue(value) == 1)
	case "after_seconds":
		return a.AfterSeconds == 0 && watchIntegerValue(value) == 0
	case "repeat_seconds":
		return a.RepeatSeconds == 0 && watchIntegerValue(value) == 0
	case "note":
		s, ok := value.(string)
		return ok && s == ""
	default:
		return false
	}
}

func watchIntegerValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return -1
	}
}

func formatWatchTriggerArgument(name string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%s=%#v", name, value)
	}
	return fmt.Sprintf("%s=%s", name, encoded)
}

func watchOperationRepairShape(a watchArgs) string {
	switch a.Operation {
	case "list":
		return `{"operation":"list"}`
	case "inspect", "clear":
		watchID := a.WatchID
		if watchID == "" {
			watchID = "watch_..."
		}
		return fmt.Sprintf(`{"operation":%q,"watch_id":%q}`, a.Operation, watchID)
	default:
		return fmt.Sprintf(`{"operation":%q}`, a.Operation)
	}
}

func watchEventFilterArg(args map[string]any) (*watchEventFilter, error) {
	raw, ok := args["event_filter"]
	if !ok || raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("event_filter must be an object")
	}
	var filter watchEventFilter
	for key, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid_request: event_filter.%s must be a string", key)
		}
		switch key {
		case "tool_name":
			filter.ToolName = strings.TrimSpace(s)
		case "status":
			filter.Status = strings.TrimSpace(s)
		default:
			return nil, fmt.Errorf("invalid_request: unknown event_filter field %q", key)
		}
	}
	if filter.ToolName == "" && filter.Status == "" {
		return nil, nil
	}
	return &filter, nil
}

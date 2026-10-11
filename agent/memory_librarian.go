package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
)

// memoryLibrarianRole is the runtime-only role that authors and gardens
// memory when the memory librarian is on. It is the one delegate that keeps
// the memory save tools, and it gets them by identity: the runtime starts it
// with delegateArgs.runtimeRole, which no model call can set.
const memoryLibrarianRole = "memory-librarian"

// memoryLibrarianMode is which arm of the memory librarian spike a root runs.
type memoryLibrarianMode string

const (
	librarianOff     memoryLibrarianMode = "off"
	librarianTurn    memoryLibrarianMode = "turn"
	librarianSidecar memoryLibrarianMode = "sidecar"
)

// memoryLibrarianEnv selects the arm when SessionConfig.MemoryLibrarian is
// empty, so `evener run` and `evener serve` pick it up without new flags.
const memoryLibrarianEnv = "EVENER_MEMORY_LIBRARIAN"

// librarianMode reports this session's librarian arm. Only a root session
// with memory on runs one; every delegate, and any unknown value, is off.
func (s *Session) librarianMode() memoryLibrarianMode {
	if s == nil || s.isSubagentSession() || s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return librarianOff
	}
	mode := s.cfg.MemoryLibrarian
	if mode == "" {
		mode = os.Getenv(memoryLibrarianEnv)
	}
	switch memoryLibrarianMode(strings.TrimSpace(strings.ToLower(mode))) {
	case librarianTurn:
		return librarianTurn
	case librarianSidecar:
		return librarianSidecar
	default:
		return librarianOff
	}
}

// selectRuntimeRoleModel selects a runtime-only role on this session's own
// model and profile.
func (s *Session) selectRuntimeRoleModel(role string) (subagentModelSelection, error) {
	agent, err := runtimeRoleAgent(role)
	if err != nil {
		return subagentModelSelection{}, err
	}
	return subagentModelSelection{agent: &agent, profile: s.currentProfile()}, nil
}

// librarianToolNameCeiling adds back to a delegate's tool ceiling the memory
// save tools that stableDelegateToolNameCeiling strips from every delegate,
// limited to the ones the role lists. The parent's memory_read stands for
// memory being on: a root under the librarian has had its own save tools
// removed (filterUnavailableMemoryTools), so they can't be looked up there.
func librarianToolNameCeiling(reg *tool.Registry, ceiling, roleTools []string) []string {
	if reg == nil || reg.Get("memory_read") == nil {
		return ceiling
	}
	out := slices.Clone(ceiling)
	for _, name := range memorySaveToolNames {
		if slices.Contains(roleTools, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// librarianReportsKept bounds how many earlier pass reports a turn pass's
// brief repeats, so each pass sees what earlier ones saved this session.
const librarianReportsKept = 6

// librarianCursor is the durable record of the librarian's progress for one
// root: the last public transcript turn a finished pass covered, the sidecar's
// delegate id, and the latest pass reports.
type librarianCursor struct {
	ProcessedThrough  int      `json:"processed_through"`
	SidecarDelegateID string   `json:"sidecar_delegate_id,omitempty"`
	Reports           []string `json:"reports,omitempty"`
}

// librarianPass is the turn pass in flight: its delegate and the turn range
// its brief named.
type librarianPass struct {
	delegateID string
	from, to   int
}

// librarianState is the root's in-process librarian bookkeeping. mu guards
// every field and is never held across a delegate call.
type librarianState struct {
	mu sync.Mutex
	// humanTurn is set by the root's human message and consumed by the turn
	// end that settles it.
	humanTurn bool
	// launching covers the gap between a turn end and its delegate existing,
	// so the one-shot drain doesn't exit before the pass starts.
	launching bool
	inFlight  *librarianPass
	// rerun asks for another pass once the one in flight reports, because a
	// human turn settled while it ran.
	rerun bool
	// sidecarNudged is set once this process has started or re-briefed the
	// sidecar.
	sidecarNudged bool
	// sidecarFramesOwedUntil bounds how long after a human turn's end the
	// one-shot drain keeps delivering the sidecar's pending watch frames.
	sidecarFramesOwedUntil time.Time
}

// librarianSidecarFrameWait bounds the one-shot drain's wait for the
// sidecar's turn-end frame, so a frame that can never land doesn't hold the
// process open.
const librarianSidecarFrameWait = 5 * time.Minute

func (s *Session) librarianCursorPath() string {
	return filepath.Join(s.stateDir, sessionsSubdir, s.id+".memory-librarian.json")
}

func (s *Session) loadLibrarianCursor() librarianCursor {
	var cursor librarianCursor
	raw, err := os.ReadFile(s.librarianCursorPath())
	if err == nil {
		_ = json.Unmarshal(raw, &cursor)
	}
	return cursor
}

func (s *Session) saveLibrarianCursor(cursor librarianCursor) {
	raw, err := json.MarshalIndent(cursor, "", "  ")
	if err == nil {
		path := s.librarianCursorPath()
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		s.emitDiagnosticWarning(warningDataFromError("memory librarian cursor save failed", err))
	}
}

// observeLibrarianEvent is the root's librarian hook on its own event stream.
// A human message marks the turn as one the librarian reads (and, under the
// sidecar arm, makes sure the sidecar is running); the turn end that settles
// it starts a turn pass. It never blocks the root: delegate work runs on its
// own goroutine.
func (s *Session) observeLibrarianEvent(kind events.EventKind) {
	if kind != events.EventUserInput && kind != events.EventTurnEnded {
		return
	}
	mode := s.librarianMode()
	if mode == librarianOff || s.delegateController == nil {
		return
	}
	st := &s.librarian
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case kind == events.EventUserInput:
		st.humanTurn = true
		if mode == librarianSidecar && !st.sidecarNudged {
			st.sidecarNudged = true
			st.launching = true
			go s.ensureLibrarianSidecar()
		}
	case mode == librarianSidecar && st.humanTurn:
		st.humanTurn = false
		st.sidecarFramesOwedUntil = s.sclock().Now().Add(librarianSidecarFrameWait)
	case mode == librarianTurn && st.humanTurn:
		st.humanTurn = false
		if st.inFlight != nil || st.launching {
			st.rerun = true
			return
		}
		st.launching = true
		go s.launchLibrarianTurnPass()
	}
}

// librarianLaunchPending reports a librarian start the runtime owes but has
// not yet made, which the one-shot drain waits for.
func (s *Session) librarianLaunchPending() bool {
	s.librarian.mu.Lock()
	defer s.librarian.mu.Unlock()
	return s.librarian.launching
}

// librarianDrainWorkLive reports librarian work the one-shot drain must treat
// as live even after the root's terminal message: a start not yet made, and,
// under the sidecar arm, the watch frames from the human turn that just ended
// (the sidecar reads that turn when its turn.end frame lands). Without it the
// drain would abandon those frames as residue and the process would exit
// before the librarian's pass.
func (s *Session) librarianDrainWorkLive() bool {
	s.librarian.mu.Lock()
	launching := s.librarian.launching
	owed := s.sclock().Now().Before(s.librarian.sidecarFramesOwedUntil)
	s.librarian.mu.Unlock()
	if launching {
		return true
	}
	return owed && s.jobManager != nil && s.jobManager.hasPendingWatchSends()
}

// publicTranscriptTurns counts the root transcript's public turns: the turn
// numbers read_transcript's range uses.
func (s *Session) publicTranscriptTurns() (int, error) {
	path := transcriptPath(s.stateDir, s.id)
	data, err := readTranscriptFull(path, filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return 0, err
	}
	return len(publicTranscriptData(data).Entries), nil
}

// launchLibrarianTurnPass starts a fresh librarian for every turn no finished
// pass has covered. A pass that fails to start, or reports a failure, leaves
// the cursor where it was, so the next pass covers its range again; one pass
// at a time means no range is read twice by passes that both succeed.
func (s *Session) launchLibrarianTurnPass() {
	st := &s.librarian
	finish := func(pass *librarianPass) {
		st.mu.Lock()
		st.launching = false
		st.inFlight = pass
		st.mu.Unlock()
		s.notify()
	}
	cursor := s.loadLibrarianCursor()
	total, err := s.publicTranscriptTurns()
	if err != nil {
		s.emitDiagnosticWarning(warningDataFromError("memory librarian could not count transcript turns", err))
		finish(nil)
		return
	}
	from := cursor.ProcessedThrough + 1
	if from > total {
		finish(nil)
		return
	}
	res := s.createDelegate(s.librarianContext(), delegateArgs{
		Task:                librarianTurnBrief(s.id, from, total, cursor.Reports),
		DelegationAllowance: new(0),
		runtimeRole:         memoryLibrarianRole,
	})
	if res.Err != nil {
		s.emitDiagnosticWarning(warningDataFromError("memory librarian pass did not start", res.Err))
		if res.DelegateID == "" {
			finish(nil)
			return
		}
	}
	finish(&librarianPass{delegateID: res.DelegateID, from: from, to: total})
}

// ensureLibrarianSidecar starts the root's sidecar librarian, or, when an
// earlier process already started one, sends it a catch-up brief: its parent
// watch did not survive the restart.
func (s *Session) ensureLibrarianSidecar() {
	defer func() {
		s.librarian.mu.Lock()
		s.librarian.launching = false
		s.librarian.mu.Unlock()
		s.notify()
	}()
	cursor := s.loadLibrarianCursor()
	ctx := s.librarianContext()
	if cursor.SidecarDelegateID != "" {
		outcome := (delegateRuntime{owner: s}).send(ctx, cursor.SidecarDelegateID, librarianSidecarResumeBrief, 0)
		if outcome.result.Err == nil {
			return
		}
		s.emitDiagnosticWarning(warningDataFromError("memory librarian sidecar resume failed; starting a new one", outcome.result.Err))
	}
	res := s.createDelegate(ctx, delegateArgs{
		Task:                librarianSidecarBrief(s.id),
		DelegationAllowance: new(0),
		WatchParent:         true,
		runtimeRole:         memoryLibrarianRole,
	})
	if res.Err != nil {
		s.emitDiagnosticWarning(warningDataFromError("memory librarian sidecar did not start", res.Err))
	}
	if res.DelegateID != "" {
		cursor = s.loadLibrarianCursor()
		cursor.SidecarDelegateID = res.DelegateID
		s.saveLibrarianCursor(cursor)
	}
}

func (s *Session) librarianContext() context.Context {
	if s.sessionCtx != nil {
		return s.sessionCtx
	}
	return context.Background()
}

// librarianReported takes a librarian delegate's report, which the runtime
// keeps instead of delivering it to the root's conversation. A turn pass that
// reported advances the cursor over its range; a failed one leaves it.
func (s *Session) librarianReported(delegateID string, packet delegatestore.TerminalPacket) {
	if s == nil || packet.Kind == delegatestore.PacketUpdate {
		return
	}
	report := librarianReportText(packet)
	st := &s.librarian
	st.mu.Lock()
	pass := st.inFlight
	if pass == nil || pass.delegateID != delegateID {
		pass = nil
	} else {
		st.inFlight = nil
	}
	rerun := pass != nil && st.rerun
	if rerun {
		st.rerun = false
		st.launching = true
	}
	st.mu.Unlock()

	cursor := s.loadLibrarianCursor()
	if pass != nil && packet.Kind == delegatestore.PacketReported {
		cursor.ProcessedThrough = max(cursor.ProcessedThrough, pass.to)
	}
	if report != "" && packet.Kind == delegatestore.PacketReported {
		cursor.Reports = append(cursor.Reports, report)
		if len(cursor.Reports) > librarianReportsKept {
			cursor.Reports = cursor.Reports[len(cursor.Reports)-librarianReportsKept:]
		}
	}
	s.saveLibrarianCursor(cursor)
	if packet.Kind != delegatestore.PacketReported {
		s.emitDiagnosticWarning(events.WarningData{Message: "memory librarian pass failed: " + report})
	}
	if rerun {
		go s.launchLibrarianTurnPass()
	}
}

func librarianReportText(packet delegatestore.TerminalPacket) string {
	var text string
	if err := json.Unmarshal(packet.Message, &text); err != nil {
		text = string(packet.Message)
	}
	return limitWatchText(strings.TrimSpace(text), watchMessageMaxChars)
}

// runtimeRoleDeliveryReceiver takes a runtime-role delegate's deliveries to
// the root. They are bookkeeping, not conversation: the root never sees them
// and they never wake it.
type runtimeRoleDeliveryReceiver struct {
	owner      *Session
	delegateID string
	packet     delegatestore.TerminalPacket
}

func (r runtimeRoleDeliveryReceiver) appendDelegateNotificationDurably(string, string) (bool, error) {
	if r.owner != nil {
		r.owner.librarianReported(r.delegateID, r.packet)
	}
	return false, nil
}

func librarianTurnBrief(rootSessionID string, from, to int, reports []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Read turns %d-%d of the main session's conversation and save what a future session needs, following your role.\n\n", from, to)
	fmt.Fprintf(&b, "Read them with read_transcript(transcript_ref=%q, range=\"%d-%d\"). Earlier turns were covered by earlier passes; read them only when a turn in your range refers back to something you need.\n", rootSessionID, from, to)
	if len(reports) > 0 {
		b.WriteString("\nWhat earlier librarian passes in this session reported, oldest first:\n")
		for _, report := range reports {
			b.WriteString("- ")
			b.WriteString(strings.ReplaceAll(report, "\n", "\n  "))
			b.WriteString("\n")
		}
	}
	b.WriteString("\nWhen you are done, end with communicate(end_turn=true) and your short report.")
	return b.String()
}

func librarianSidecarBrief(rootSessionID string) string {
	return fmt.Sprintf(`You are the long-lived memory librarian for the main session %[1]q. You stay with it across its turns and keep your own notes in this conversation.

First, subscribe to the main session:
job_watch(operation="create", source="parent", events=["user.message", "turn.end"], note="memory librarian: read the main session's new turns")

Then do one pass now, and one pass each time a frame wakes you:
1. Read the main session's turns you have not read yet with read_transcript(transcript_ref=%[1]q, range="N-M"). Keep track of the last turn number you read; the transcript's turn numbers only grow. A user.message frame means a turn is starting; you may save an explicit "remember this" right away, but most of the evidence arrives by the turn.end frame.
2. Decide and write, following your role.
3. End the pass with communicate(end_turn=true) and your short report. That ends your turn until the next frame.

If a frame or notice says your watch ended, create it again with the same call before you read.`, rootSessionID)
}

const librarianSidecarResumeBrief = `The main session restarted, so your watch on it ended. Create it again with job_watch(operation="create", source="parent", events=["user.message", "turn.end"], note="memory librarian: read the main session's new turns"), then read any turns you have not read yet, write what your role says to keep, and end with communicate(end_turn=true) and your short report.`

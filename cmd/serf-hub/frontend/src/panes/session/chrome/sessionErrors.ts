// Shared wire-rejection classifiers for the session-chrome panels
// (TasksPanel, JobsPanel). Both panels fetch their row lists through the
// same hub resume-and-forward path and so classify the same two EXPECTED
// rejections identically: a source that cannot answer the call at all
// (actionUnavailable - an expected capability gap, not a bug, so it earns an
// honest inline "not available" state and no toast) and a thread the hub
// never tracked at all (isThreadNotFound - terminal, no retry). Extracted
// from TasksPanel.tsx when JobsPanel.tsx needed the same two checks - one
// definition, so both panels can never drift apart on what counts as
// recoverable.
import { WireError } from "../../../protocol/errors";

export function isActionUnavailable(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "actionUnavailable";
}

// A local-source thread with no live daemon behind it (a one-shot CLI
// session that already exited, or one never resumed) rejects the panel's
// list call with appwire.SessionUnavailable("thread not found: " + threadID)
// - the ONLY call site that prefixes a sessionUnavailable message this way
// (cmd/serf-hub/internal/appsource/local_daemon.go:551, entryForRef finding
// no matching rendezvous entry). A live daemon that's merely unreachable for
// a moment (connection reset, broken pipe, i/o timeout) is ALSO
// sessionUnavailable, but as "local/codex daemon unavailable: ..."
// (local_daemon.go:438-501, codex_source.go:522-591) - that must still
// surface as a real error, so this checks the message prefix too, not just
// the serfErrorInfo code.
//
// This is also why the panels' daemonGone terminal state never offers Try
// again: the hub's own list handlers (app_tasks.go's hubTasksList and the
// jobs equivalent) already fall back to a persisted past-index read before
// this error ever reaches the wire, so a rejection actually shaped like this
// means the hub found NEITHER a live daemon NOR a past-index record for the
// thread. That is also exactly the condition withSessionResume's hubKnowsRef
// gate checks before attempting any resume (app_session_resume.go) - so
// nothing on either end of the wire can bring this session back. Retrying
// would fail identically forever.
export function isThreadNotFound(err: unknown): boolean {
  return (
    err instanceof WireError &&
    err.serfErrorInfo === "sessionUnavailable" &&
    err.message.startsWith("thread not found: ")
  );
}

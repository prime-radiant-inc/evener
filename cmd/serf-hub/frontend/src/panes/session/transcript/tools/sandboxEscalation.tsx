// Sandbox escalation cards (M7): "the tool-exec goroutine blocks until
// answered via serf/sandbox/escalation/resolve" (docs/appwire-protocol.md).
// Fully interactive - approve/deny actually call the wire method - but NOT
// wired into the live transcript tree. Ground truth (see the wave-4 task-3
// report for the full trail, condensed here):
//
//   - serf/sandbox/escalation/requested and SerfThread.pendingEscalations
//     are thread-level (a raw notification + a snapshot list on the
//     hydrate response), never a ThreadItem. R3 projects both into
//     ThreadModel.pendingEscalations (protocol/reducer.ts's hydrateThread
//     seeds it from the snapshot; applyNotification's own case keeps it
//     live-updated) - this file reads that model instead of tracking its
//     own local copy.
//   - Neither ItemRenderProps nor ToolRenderProps carries a ref or the
//     owning ThreadModel (ToolCallItem.tsx receives `turn` but drops it
//     before calling a descriptor's body), so there is no
//     registerToolRenderer/registerItemRenderer integration point for a
//     thread-level card at all - every other T3 surface hangs off a tool
//     call item; this one structurally can't.
//   - The legacy renderer mounts this as a direct sibling of turns in the
//     conversation container (appendSandboxEscalation), not nested in any
//     tool row - confirming it was never meant to be item-scoped even in
//     the original design.
//
// Given that, this file ships a fully working, fully tested card + data
// hook, ready for whoever owns the mount point (a Session.tsx-level slot
// is outside transcript/tools/**'s ownership).  Reading off the shared
// threads store (rather than a private subscription) also means a second
// browser tab/the CLI resolving the SAME escalation converges once this
// client's own next snapshot or live notification touches that model -
// still no "escalation resolved" broadcast on the wire (only this client's
// own successful resolve() removes its local copy - see stores/threads.ts's
// resolveEscalation), but no worse than before, and the common cold-open
// case now works without any resolve happening at all.
import { useCallback, useState } from "react";
import { sessionActionError } from "../../../../protocol/errors";
import type { SandboxEscalationRequested } from "../../../../protocol/types.gen";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, Card, Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./sandboxescalation.module.css";

const CLASS = {
  label: requireClass(styles.label, "sandboxescalation.module.css", "label"),
  body: requireClass(styles.body, "sandboxescalation.module.css", "body"),
  error: requireClass(styles.error, "sandboxescalation.module.css", "error"),
  actions: requireClass(styles.actions, "sandboxescalation.module.css", "actions"),
};

export interface SandboxEscalationCardProps {
  escalation: SandboxEscalationRequested;
  onApprove(): void;
  onDeny(): void;
  resolved: boolean;
  error?: string;
}

// SandboxEscalationCard is deliberately styled and labeled as a HARNESS
// prompt, never a model message - mirrors the legacy card's own stated
// intent (renderer.js's appendSandboxEscalation comment): the model can
// neither emit nor influence this card, so a human reading it should
// never mistake it for something the agent said.
export function SandboxEscalationCard({ escalation, onApprove, onDeny, resolved, error }: SandboxEscalationCardProps) {
  return (
    <Card>
      <div className={CLASS.label}>Sandbox approval — requested by serf, not the agent</div>
      <div className={CLASS.body}>
        The sandbox blocked {escalation.tool} from accessing {escalation.deniedPath} [--sandbox {escalation.mode}].
        Allow this one action?
      </div>
      {/* Danger treatment comes entirely from Chip's own tone prop (already
          allowlisted in token-contract.test.ts) - this file's own CSS module
          stays tokens-only with no bare --danger reference of its own. */}
      {error !== undefined && (
        <div className={CLASS.error}>
          <Chip tone="danger">Failed</Chip> {error}
        </div>
      )}
      <div className={CLASS.actions}>
        <Button variant="primary" size="sm" onClick={onApprove} disabled={resolved}>
          Allow
        </Button>
        <Button variant="danger" size="sm" onClick={onDeny} disabled={resolved}>
          Deny
        </Button>
      </div>
    </Card>
  );
}

export interface UseSandboxEscalationsResult {
  pending: SandboxEscalationRequested[];
  resolve(escalationId: string, approve: boolean): Promise<void>;
}

const NO_PENDING_ESCALATIONS: SandboxEscalationRequested[] = [];

// useSandboxEscalations reads `ref`'s tracked ThreadModel.pendingEscalations
// (snapshot-seeded at hydrate, live-updated by serf/sandbox/escalation/
// requested - both protocol/reducer.ts's job) and delegates resolve() to
// the threads store's own resolveEscalation action. `ref` must already be
// ensureThread'd by something else (a real session pane) - this hook only
// reads the shared model, it does not hydrate one of its own.
export function useSandboxEscalations(ref: string): UseSandboxEscalationsResult {
  const pending = useThreadsStore((s) => s.threads.get(ref)?.pendingEscalations ?? NO_PENDING_ESCALATIONS);

  const resolve = useCallback(
    (escalationId: string, approve: boolean) => threadsStore.getState().resolveEscalation(ref, escalationId, approve),
    [ref],
  );

  return { pending, resolve };
}

// SandboxEscalationRail is the ready-to-mount combination of the hook and
// the card, one per pending escalation - whoever owns the actual mount
// point (see this file's own header) needs only render
// <SandboxEscalationRail sessionRef={sessionRef} />. The prop is
// `sessionRef`, not `ref` - the latter is reserved (React would try to
// interpret a plain string value as an actual ref attempt).
export function SandboxEscalationRail({ sessionRef }: { sessionRef: string }) {
  const { pending, resolve } = useSandboxEscalations(sessionRef);
  // Tracks escalations with a resolve() request currently in flight, so a
  // card disables the instant it's clicked rather than only once the
  // response arrives - resolve() itself also removes a SETTLED escalation
  // from `pending` entirely, so "in flight" is the only state this needs
  // to track locally.
  const [resolving, setResolving] = useState<Set<string>>(new Set());
  // Tracks the last resolve() failure per escalation, so a rejection - the
  // sandbox unreachable, the wire request itself failing - surfaces on the
  // card instead of becoming an unhandled rejection (onApprove/onDeny below
  // call handleResolve fire-and-forget). Cleared at the start of every new
  // attempt so a retry never shows a stale message while it's in flight, and
  // a SUCCESSFUL resolve never has to clear it explicitly: resolve() itself
  // drops the escalation from `pending`, which unmounts the card.
  const [errors, setErrors] = useState<Map<string, string>>(new Map());

  async function handleResolve(escalationId: string, approve: boolean) {
    setResolving((prev) => new Set(prev).add(escalationId));
    setErrors((prev) => {
      if (!prev.has(escalationId)) return prev;
      const next = new Map(prev);
      next.delete(escalationId);
      return next;
    });
    try {
      await resolve(escalationId, approve);
    } catch (err) {
      setErrors((prev) => new Map(prev).set(escalationId, sessionActionError("Couldn't resolve", err)));
    } finally {
      setResolving((prev) => {
        const next = new Set(prev);
        next.delete(escalationId);
        return next;
      });
    }
  }

  return (
    <>
      {pending.map((escalation) => (
        <SandboxEscalationCard
          key={escalation.escalationId}
          escalation={escalation}
          onApprove={() => void handleResolve(escalation.escalationId, true)}
          onDeny={() => void handleResolve(escalation.escalationId, false)}
          resolved={resolving.has(escalation.escalationId)}
          error={errors.get(escalation.escalationId)}
        />
      ))}
    </>
  );
}

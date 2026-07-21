// Sandbox escalation cards (M7): "the tool-exec goroutine blocks until
// answered via serf/sandbox/escalation/resolve" (docs/appwire-protocol.md).
// Fully interactive this wave, per the task - approve/deny actually call
// the wire method - but NOT wired into the live transcript tree. Ground
// truth (see the wave-4 task-3 report for the full trail, condensed here):
//
//   - serf/sandbox/escalation/requested and SerfThread.pendingEscalations
//     are thread-level (a raw notification + a snapshot list on the
//     hydrate response), never a ThreadItem. protocol/reducer.ts's
//     applyNotification has no case for the notification (falls to
//     `default`, a no-op) and hydrateThread never reads
//     thread.serf.pendingEscalations - confirmed by reading both
//     functions directly, not assumed.
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
// is outside transcript/tools/**'s ownership - T4 has SessionPane edit
// rights per the wave-4 plan, or this lands at wave-close integration).
// Two gaps remain even once mounted, both requiring changes outside this
// file's ownership: (1) the useSandboxEscalations hook below only ever
// learns about a LIVE notification - an escalation already pending before
// this hook mounts (the reconnect/cold-open case) needs
// thread.serf.pendingEscalations projected into ThreadModel first, which
// only protocol/reducer.ts can do; (2) resolving inline elsewhere (a
// different browser tab, the CLI) never reaches THIS hook's local pending
// list at all - there is no "escalation resolved" notification to react
// to, only the request/response pair this file drives itself.
import { useCallback, useEffect, useState } from "react";
import { Button, Card } from "../../../../widgets";
import { useClient } from "../../../../shell/clientContext";
import type { AnyNotification, SandboxEscalationRequested } from "../../../../protocol/types.gen";
import styles from "./sandboxescalation.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";

const CLASS = {
  label: requireClass(styles.label, "sandboxescalation.module.css", "label"),
  body: requireClass(styles.body, "sandboxescalation.module.css", "body"),
  actions: requireClass(styles.actions, "sandboxescalation.module.css", "actions"),
};

export interface SandboxEscalationCardProps {
  escalation: SandboxEscalationRequested;
  onApprove(): void;
  onDeny(): void;
  resolved: boolean;
}

// SandboxEscalationCard is deliberately styled and labeled as a HARNESS
// prompt, never a model message - mirrors the legacy card's own stated
// intent (renderer.js's appendSandboxEscalation comment): the model can
// neither emit nor influence this card, so a human reading it should
// never mistake it for something the agent said.
export function SandboxEscalationCard({ escalation, onApprove, onDeny, resolved }: SandboxEscalationCardProps) {
  return (
    <Card>
      <div className={CLASS.label}>Sandbox approval — requested by serf, not the agent</div>
      <div className={CLASS.body}>
        The sandbox blocked {escalation.tool} from accessing {escalation.deniedPath} [--sandbox {escalation.mode}].
        Allow this one action?
      </div>
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

function isEscalationRequested(n: AnyNotification): n is AnyNotification & { params: SandboxEscalationRequested } {
  return n.method === "serf/sandbox/escalation/requested";
}

// useSandboxEscalations subscribes to LIVE serf/sandbox/escalation/
// requested notifications for `ref` for as long as it's mounted, and
// exposes resolve() to answer one via serf/sandbox/escalation/resolve -
// see this file's own header for the two documented gaps (no snapshot
// seeding, no cross-tab resolution sync) neither of which this hook can
// close on its own.
export function useSandboxEscalations(ref: string): UseSandboxEscalationsResult {
  const client = useClient();
  const [pending, setPending] = useState<SandboxEscalationRequested[]>([]);

  useEffect(() => {
    return client.onNotification((n) => {
      if (!isEscalationRequested(n)) return;
      const escalation = n.params;
      if (escalation.ref !== ref) return;
      setPending((prev) => (prev.some((e) => e.escalationId === escalation.escalationId) ? prev : [...prev, escalation]));
    });
  }, [client, ref]);

  const resolve = useCallback(
    async (escalationId: string, approve: boolean) => {
      await client.request("serf/sandbox/escalation/resolve", { ref, escalationId, approve });
      setPending((prev) => prev.filter((e) => e.escalationId !== escalationId));
    },
    [client, ref],
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

  async function handleResolve(escalationId: string, approve: boolean) {
    setResolving((prev) => new Set(prev).add(escalationId));
    try {
      await resolve(escalationId, approve);
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
        />
      ))}
    </>
  );
}

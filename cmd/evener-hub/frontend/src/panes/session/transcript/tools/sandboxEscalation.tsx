// Sandbox escalation cards (M7): "the tool-exec goroutine blocks until
// answered via evener/sandbox/escalation/resolve" (docs/appwire-protocol.md).
// Fully interactive - approve/deny actually call the wire method - and
// mounted at the SESSION level (Session.tsx renders SandboxEscalationRail),
// deliberately not inside the transcript tree. Ground truth for why a
// transcript mount is structurally impossible (see the wave-4 task-3
// report for the full trail, condensed here):
//
//   - evener/sandbox/escalation/requested and EvenerThread.pendingEscalations
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
// Given that, this file owns the card + data hook and Session.tsx owns the
// mount (a Session.tsx-level slot is outside transcript/tools/**'s
// ownership).  Reading off the shared
// threads store (rather than a private subscription) also means a second
// browser tab/the CLI resolving the SAME escalation converges once this
// client's own next snapshot or live notification touches that model -
// still no "escalation resolved" broadcast on the wire (only this client's
// own successful resolve() removes its local copy - see stores/threads.ts's
// resolveEscalation), but no worse than before, and the common cold-open
// case now works without any resolve happening at all.
import { type KeyboardEvent as ReactKeyboardEvent, useCallback, useEffect, useRef, useState } from "react";
import { sessionActionError } from "../../../../protocol/errors";
import type { SandboxEscalationRequested } from "../../../../protocol/types.gen";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./sandboxescalation.module.css";

const CLASS = {
  card: requireClass(styles.card, "sandboxescalation.module.css", "card"),
  rail: requireClass(styles.rail, "sandboxescalation.module.css", "rail"),
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
//
// Amber envelope, not a neutral Card (reviewed UX fix): this is a BLOCKING
// approval - the tool-exec goroutine is parked waiting on it - the app's
// SECOND "a human is needed right now" moment after askDock's ask batch, so
// it gets the same container treatment (sandboxescalation.module.css's
// .card, allowlisted in token-contract.test.ts's SEMANTIC_PATH_EXCEPTIONS
// for the same structural reason askdock.module.css is).
//
// Mod+Enter approves (handleKeyDown below); Escape deliberately does NOT
// deny - denial must never be one accidental keypress away, so Deny stays
// mouse/tab-reachable only.
export function SandboxEscalationCard({ escalation, onApprove, onDeny, resolved, error }: SandboxEscalationCardProps) {
  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey)) return;
    event.preventDefault();
    if (!resolved) onApprove();
  }

  return (
    // role="group" + Mod+Enter's onKeyDown makes this an interactive
    // grouping, not a hand-rolled generic <div> - the WAI-ARIA "group"
    // role is the right one for a set of controls with a shared
    // keybinding, and there is no native element for it.
    // biome-ignore lint/a11y/useSemanticElements: role="group" is deliberate, see above
    <div className={CLASS.card} role="group" aria-label="Sandbox approval" onKeyDown={handleKeyDown}>
      <div className={CLASS.label}>Sandbox approval — requested by evener, not the agent</div>
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
        <Button variant="primary" size="sm" onClick={onApprove} disabled={resolved} data-sandbox-escalation-allow>
          Allow
        </Button>
        <Button variant="danger" size="sm" onClick={onDeny} disabled={resolved}>
          Deny
        </Button>
      </div>
    </div>
  );
}

export interface UseSandboxEscalationsResult {
  pending: SandboxEscalationRequested[];
  resolve(escalationId: string, approve: boolean): Promise<void>;
}

const NO_PENDING_ESCALATIONS: SandboxEscalationRequested[] = [];

// True when `element` is a text-entry surface a reader could be mid-keystroke
// in - a plain <input>/<textarea> or anything marked contenteditable (the
// composer's own editable surface). Used by the autofocus effect below to
// never yank focus off whatever the reader is actively typing into.
function isEditableElement(element: Element | null): boolean {
  if (element === null) return false;
  const tag = element.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA") return true;
  return (element as HTMLElement).isContentEditable === true;
}

// useSandboxEscalations reads `ref`'s tracked ThreadModel.pendingEscalations
// (snapshot-seeded at hydrate, live-updated by evener/sandbox/escalation/
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

  // Autofocuses the Allow button the moment an escalation arrives (empty ->
  // non-empty), edge-triggered on pending.length exactly like AskDock.tsx's
  // own dock-activation effect - so a LATER escalation that only grows an
  // already-open rail never steals focus from a card already on screen. A
  // plain [data-sandbox-escalation-allow] query on the rail's own root finds
  // the first pending card's Allow button without threading a ref down into
  // SandboxEscalationCard for a one-time, edge-triggered action.
  //
  // Skipped entirely while the reader is mid-keystroke in an editable surface
  // (the composer's textarea, most likely): this same edge fires on mount as
  // well as empty->non-empty, so a cold-open escalation arriving while
  // someone is typing must not steal focus onto Allow - the very next
  // Enter/Space they type would approve an escalation they haven't read yet.
  const railRef = useRef<HTMLDivElement>(null);
  const wasEmptyRef = useRef(true);
  useEffect(() => {
    const isEmpty = pending.length === 0;
    const wasEmpty = wasEmptyRef.current;
    wasEmptyRef.current = isEmpty;
    if (!wasEmpty || isEmpty) return;
    if (isEditableElement(document.activeElement)) return;
    railRef.current?.querySelector<HTMLElement>("[data-sandbox-escalation-allow]")?.focus();
  }, [pending.length]);

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

  // Nothing pending renders nothing at all - not even the sticky rail
  // wrapper, which would otherwise sit in the scroll body as an empty,
  // zero-height sticky element.
  if (pending.length === 0) return null;

  return (
    <div className={CLASS.rail} ref={railRef}>
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
    </div>
  );
}

// ComprehensionView.tsx — Mode 2: full-screen overlay showing the session
// tree's rails side-by-side on one shared live clock. Parent leftmost,
// subagents by most-recent-activity with 60s hysteresis + FLIP animation.
//
// Following the OverlayPanel pattern (scrim, aria-modal, Escape, FocusScope).
// Each rail is the Mode-1 SessionRail at its exact 156px width. Click-through
// exits the overlay and opens that session's transcript at the exact turn.

import { useEffect, useMemo, useState } from "react";
import { threadsStore } from "../../../stores/threads";
import { findSessionNode, type TreeNode, useTreeStore } from "../../../stores/tree";
import { FocusScope } from "../../../widgets/focusscope";
import { useNowTick } from "../liveness";
import styles from "./comprehensionView.module.css";
import { desiredCompOrder, type OrderableSession } from "./ordering";
import { railModelFromTurns } from "./railModel";
import { SessionRail } from "./SessionRail";
import { useRailTheme } from "./useRailTheme";

export interface ComprehensionViewProps {
  open: boolean;
  onClose: () => void;
  /** The parent session's ref (always leftmost). */
  parentRef: string;
  /** Called when the user clicks a rail to open that session at a turn. */
  onOpenSession: (ref: string, turnIndex?: number) => void;
}

/** Collect all session refs from the tree, starting from the parent. */
function collectSessionRefs(node: TreeNode, isParent: boolean): { ref: string; node: TreeNode; isParent: boolean }[] {
  const out: { ref: string; node: TreeNode; isParent: boolean }[] = [];
  if (node.ref) {
    out.push({ ref: node.ref, node, isParent });
  }
  for (const child of node.children) {
    out.push(...collectSessionRefs(child, false));
  }
  return out;
}

export function ComprehensionView({ open, onClose, parentRef, onOpenSession }: ComprehensionViewProps) {
  const theme = useRailTheme();
  const now = useNowTick(1000);
  const tree = useTreeStore((s) => s.tree);
  const [order, setOrder] = useState<number[]>([]);

  // Collect all sessions from the tree (parent + children).
  const sessions = useMemo(() => {
    if (!tree) return [];
    const parent = findSessionNode(tree, parentRef);
    if (!parent) return [];
    return collectSessionRefs(parent, true);
  }, [tree, parentRef]);

  // Hydrate all sessions via ensureThread on open, release on close.
  useEffect(() => {
    if (!open) return;
    const refs = sessions.map((s) => s.ref);
    for (const ref of refs) {
      threadsStore
        .getState()
        .ensureThread(ref)
        .catch(() => {});
    }
    return () => {
      for (const ref of refs) {
        threadsStore.getState().releaseThread(ref);
      }
    };
  }, [open, sessions]);

  // Build orderable sessions for the ordering algorithm.
  const orderableSessions: OrderableSession[] = useMemo(
    () =>
      sessions.map((s, i) => ({
        index: i,
        isParent: s.isParent,
        lastActivityMs: s.node.last_activity_at ?? 0,
      })),
    [sessions],
  );

  // Compute desired order with hysteresis.
  const initialOrder = useMemo(() => sessions.map((_, i) => i), [sessions]);
  useEffect(() => {
    if (sessions.length === 0) return;
    const next = desiredCompOrder(orderableSessions, now, order.length > 0 ? order : initialOrder);
    setOrder(next);
  }, [orderableSessions, now, order, sessions.length, initialOrder]);

  // Escape to close.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open || sessions.length === 0) return null;

  return (
    <FocusScope>
      <div className={styles.overlay} role="dialog" aria-modal="true" aria-label="Comprehension view">
        <button
          type="button"
          className={styles.scrim}
          onClick={onClose}
          aria-label="Close comprehension view"
          tabIndex={-1}
        />
        <div className={styles.panel}>
          <header className={styles.header}>
            <h2 className={styles.title}>
              Comprehension · <span className={styles.accent}>{sessions.length} sessions · shared live clock</span>
            </h2>
            <button
              type="button"
              className={styles.closeButton}
              onClick={onClose}
              aria-label="Close comprehension view (Escape)"
            >
              ✕ Exit (Esc)
            </button>
          </header>
          <div className={styles.railsContainer}>
            {order.map((idx) => {
              const session = sessions[idx];
              if (!session) return null;
              return (
                <ComprehensionRail
                  key={session.ref}
                  ref_={session.ref}
                  isParent={session.isParent}
                  theme={theme}
                  now={now}
                  onOpenSession={onOpenSession}
                />
              );
            })}
          </div>
          <footer className={styles.footer}>
            Shared live clock — y is elapsed-since-start over [0, now] (min 10 min window). All rails re-scale together.
            NOW-lines stay aligned. Parent leftmost, then most-recent-activity (60s hysteresis). Nothing is drawn before
            its time.
          </footer>
        </div>
      </div>
    </FocusScope>
  );
}

/** One rail in the comprehension view. */
function ComprehensionRail({
  ref_,
  isParent,
  theme,
  now,
  onOpenSession,
}: {
  ref_: string;
  isParent: boolean;
  theme: ReturnType<typeof useRailTheme>;
  now: number;
  onOpenSession: (ref: string, turnIndex?: number) => void;
}) {
  const model = threadsStore.getState().threads.get(ref_);
  const railModel = useMemo(() => (model ? railModelFromTurns(model.turns, now) : null), [model, now]);

  if (!model || !railModel) {
    return (
      <div className={styles.railPlaceholder}>
        <div className={styles.railHead}>{isParent ? "PARENT" : "SUBAGENT"}</div>
        <div className={styles.railLoading}>Loading…</div>
      </div>
    );
  }

  return (
    <div className={styles.railWrapper}>
      <button
        type="button"
        className={styles.railHead}
        onClick={() => onOpenSession(ref_)}
        aria-label={`Open session ${ref_}`}
      >
        <span className={styles.railLabel}>{isParent ? "PARENT" : "SUBAGENT"}</span>
        <span className={styles.railId}>{ref_.slice(-8)}</span>
      </button>
      <SessionRail
        model={railModel}
        nowMs={now}
        axis="shared"
        theme={theme}
        thumb={null}
        playing={model.status?.type === "active"}
        ended={model.status?.type === "ended"}
        onJump={(idx) => onOpenSession(ref_, idx)}
      />
      <div className={styles.railFoot}>
        {railModel.live.n} turns · Σ {railModel.live.burn} tok
      </div>
    </div>
  );
}

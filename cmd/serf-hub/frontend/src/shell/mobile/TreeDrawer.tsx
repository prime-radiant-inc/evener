// The mobile tree drawer: a trigger button in StackHost's top bar plus the
// Sheet it opens. Slot contract for the sibling stream building the real
// rail (src/shell/rail/**, owned by a different task - this stream may not
// touch that directory, and that one may not touch this one, so the two
// meet only through props, wired up by whoever merges both): pass the real
// rail component as `children` -
//
//   <TreeDrawer><Rail /></TreeDrawer>
//
// - and it replaces the placeholder below outright, with no other change
// needed here. `children` is deliberately the ENTIRE contract (no bespoke
// "onSelect"/"close" callback prop): the rail only needs to call
// workspaceStore's own openPane(), exactly like every other pane-opening
// trigger in the app already does (Welcome's "New session" button,
// AppShell's routing glue, DockHost's dockview interactions) - the
// auto-close effect below already reacts to that, for free, with no
// bespoke integration code required on the rail's side.
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTreeStore } from "../../stores/tree";
import { Badge, EmptyState, IconButton, Sheet } from "../../widgets";
import { useWorkspaceStore } from "../workspace";
import styles from "./treedrawer.module.css";

function SessionsIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M2 4 H14 M2 8 H14 M2 12 H14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

export interface TreeDrawerProps {
  children?: ReactNode;
}

export function TreeDrawer({ children }: TreeDrawerProps) {
  const [open, setOpen] = useState(false);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const prevFocusedIdRef = useRef(focusedPaneId);
  // UX fix: parity with RailHost's own hidden-rail ☰ chip, which already
  // shows Badge(needsYou) beside its glyph - this trigger carries the same
  // Badge, overlaid on its corner since an icon-only button has no room for
  // an inline second element.
  //
  // FIX 4: reads tree.needs_you (the server's own ordered needs-you list -
  // needsYouCycle.ts's own "one source of truth" for every other needs-you
  // surface in the app), NOT attentionSummary.needsYou - a separate,
  // narrower aggregate (hubcore's DeriveAttention/tierEligible: top-level,
  // non-subagent, non-archived sessions only) that can undercount relative
  // to what the drawer's own rows show once opened, leaving the trigger
  // unbadged for a session the rows plainly present as needing you
  // (real-browser observation).
  const needsYou = useTreeStore((s) => s.tree?.needs_you.length ?? 0);

  // Auto-close on navigation: whatever eventually fills the slot above
  // only ever needs to call openPane() for this to work - see the module
  // comment. Keyed specifically on focusedPaneId's VALUE changing, not on
  // "the store emitted some update" more broadly (e.g. re-opening the
  // already-focused singleton with different params updates the store but
  // leaves focusedPaneId itself unchanged - must not close the drawer).
  useEffect(() => {
    if (open && prevFocusedIdRef.current !== focusedPaneId) setOpen(false);
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open]);

  return (
    <>
      <span className={styles.triggerWrap}>
        <IconButton label="Sessions" icon={<SessionsIcon />} variant="quiet" onClick={() => setOpen(true)} />
        {needsYou > 0 && (
          <span className={styles.badgeOverlay}>
            <Badge count={needsYou} tone="attention" />
          </span>
        )}
      </span>
      {/* Bottom, not right: see this stream's own report for the
          thumb-reach justification (a bottom sheet's header/close button
          sits within one-handed reach; a full-height right-edge sheet's
          header does not, on a tall phone). */}
      <Sheet side="bottom" open={open} onClose={() => setOpen(false)} title="Sessions">
        {children ?? (
          // A single descriptive title, no separate hint - same "not built
          // yet" placeholder convention as panes/session/Session.tsx's own
          // EmptyState, and avoids repeating "Sessions" right under the
          // Sheet's own title heading above.
          <div data-testid="rail-slot">
            <EmptyState title="Session tree arrives from a parallel wave-3 stream" />
          </div>
        )}
      </Sheet>
    </>
  );
}

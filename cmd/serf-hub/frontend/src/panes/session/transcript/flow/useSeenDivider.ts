// useSeenDivider (kata g2ez): answers "reopening a session you stepped away
// from shows no marker for what's new" by giving the transcript a single,
// fixed "you left off here" divider turn id.
//
// Scope decisions (each was an open product question the filing agent
// deliberately left unsettled - see the kata's own comments):
//  1. SCOPE: per-session, keyed by sessionRef - matches the existing
//     per-ref localStorage precedent (composer/draft.ts's
//     serf.composer.draft.v1.<ref>), not per-pane or per-device.
//  2. PERSISTENCE: localStorage, so device-local. A laptop-only watermark
//     never clears on a phone or second machine - a real limitation, but a
//     cross-device read cursor would need to live on the daemon (wire
//     protocol + server persistence), a materially bigger feature that
//     should not block this one.
//  3. TWO TABS ON ONE SESSION: not coordinated (this app deliberately has
//     no cross-tab sync - see notifications/leader.ts). Each tab's unmount
//     overwrites the stored watermark independently; last-unmount-wins.
//     Shipped as a known gap, same as the two similar gaps the wave-2
//     design doc already documents.
//
// Mechanics: the watermark is the id of the last turn that existed the
// last time this pane was mounted, written on UNMOUNT (not continuously -
// matches useTranscriptScroll's own scroll-position flushPendingSave
// pattern: best-effort, not durable against a hard browser close/crash
// mid-view). dockview unmounts a pane's whole tree whenever its tab isn't
// active (Session.tsx's own top-of-file comment), so switching to another
// pane and back - not just closing the browser - already exercises this
// same unmount/mount cycle, which is exactly the "stepped away, came back"
// case this exists for.
//
// The divider turn id is computed ONCE per mount, from the FIRST render
// that has turns loaded, and never recomputed afterward: it names a fixed
// point in the transcript (the turn right after the reader's last-seen
// one), not a live boundary that should keep sliding as more content
// streams in during THIS viewing - that live-while-open case is
// NewContentPill's job, not this one's. Because it's stored as a turn ID
// (not an index), it stays valid across prepends (loadOlder) without any
// shifting logic of its own.
//
// If the stored watermark turn isn't found in the loaded turns (never
// visited before, or - same reasoning as useTranscriptScroll's own
// full-resync branch - a shape too different to confidently place a
// marker in), this shows no divider rather than guessing.
import { useEffect, useRef, useState } from "react";
import type { ThreadModel } from "../../../../protocol/model";
import { readSeenWatermark, writeSeenWatermark } from "./seenWatermark";

export function useSeenDivider(ref: string, model: ThreadModel | undefined): string | null {
  const [dividerTurnId, setDividerTurnId] = useState<string | null>(null);
  const computedRef = useRef(false);
  // Latest-ref mirror so the unmount cleanup (attached once, deps=[ref])
  // writes the CURRENT model's last turn, not whatever model existed at
  // mount - same "long-lived closure needs a fresh read" reasoning as
  // useTranscriptScroll's own loadOlderRef/modelRef.
  const modelRef = useRef(model);
  modelRef.current = model;

  useEffect(() => {
    if (computedRef.current) return;
    if (!model || model.turns.length === 0) return;
    computedRef.current = true;
    const watermark = readSeenWatermark(ref);
    if (watermark === null) return;
    const idx = model.turns.findIndex((t) => t.id === watermark);
    if (idx === -1 || idx >= model.turns.length - 1) return;
    const next = model.turns[idx + 1];
    if (next) setDividerTurnId(next.id);
  }, [ref, model]);

  useEffect(() => {
    return () => {
      const turns = modelRef.current?.turns;
      const last = turns && turns.length > 0 ? turns[turns.length - 1] : undefined;
      if (last) writeSeenWatermark(ref, last.id);
    };
  }, [ref]);

  return dividerTurnId;
}

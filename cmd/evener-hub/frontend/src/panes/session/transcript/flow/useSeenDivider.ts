// useSeenDivider: the boundary between what this browser has shown of a
// session and what arrived while its pane was away. One fixed turn id per
// open, which the transcript marks with the "New since your last visit"
// divider and, when that turn has finished, opens at.
//
// The watermark is the latest settled turn at the moment the reader leaves:
// a turn still running then has not been read to its end, so the reply it
// goes on to finish counts as new. It is per session and per browser
// (localStorage); two tabs on one session each overwrite it, last leave wins.
//
// The boundary is decided once per open, from the first render that has the
// conversation's turns, and never moves while the pane stays on that session:
// content arriving during this viewing is the new-content pill's concern. It
// is a turn id, so it survives older pages loading above it. A watermark turn
// that is not among the loaded turns places no boundary rather than a guess.
//
// A pane is reused across sessions without remounting, so "leaving" is the
// session ref changing as much as the pane unmounting, and what is written is
// the last turn list rendered for the ref being left.

import { displayTurnStatus, isInProgressStatus, type ThreadModel } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import { isDormantTranscript } from "../transcriptVisibility";
import { readSeenWatermark, writeSeenWatermark } from "./seenWatermark";

export interface SeenBoundary {
  /** The first turn this browser has not shown to its end, or null. */
  dividerTurnId: string | null;
  /** Whether that turn had finished when the session opened: a finished
   * unseen reply is where the session opens; one still running opens at the
   * live end like any other session. */
  settled: boolean;
}

const NO_BOUNDARY: SeenBoundary = { dividerTurnId: null, settled: false };

function isSettled(turn: { id: string; status: string }, model: ThreadModel): boolean {
  return !isInProgressStatus(displayTurnStatus(turn, model));
}

function latestSettledTurnId(model: ThreadModel): string | undefined {
  for (let index = model.turns.length - 1; index >= 0; index -= 1) {
    const turn = model.turns[index];
    if (turn && isSettled(turn, model)) return turn.id;
  }
  return undefined;
}

function boundaryFor(ref: string, model: ThreadModel): SeenBoundary {
  const watermark = readSeenWatermark(ref);
  if (watermark === null) return NO_BOUNDARY;
  const seen = model.turns.findIndex((turn) => turn.id === watermark);
  const next = seen === -1 ? undefined : model.turns[seen + 1];
  return next ? { dividerTurnId: next.id, settled: isSettled(next, model) } : NO_BOUNDARY;
}

/** The turn a session opens at instead of the live end: a finished reply this
 * browser has not shown. The live end keeps the opening while something waits
 * there for the reader (a question or an approval), and a reader with a
 * retained place of their own up the transcript returns to it. */
export function unseenOpeningTurnId(
  boundary: SeenBoundary,
  reader: { waitingAtEnd: boolean; retained: { followingBottom: boolean } | undefined },
): string | null {
  if (!boundary.settled || reader.waitingAtEnd) return null;
  if (reader.retained !== undefined && !reader.retained.followingBottom) return null;
  return boundary.dividerTurnId;
}

export function useSeenDivider(ref: string, model: ThreadModel | undefined): SeenBoundary {
  // Decided during render, not in an effect: the transcript's opening scroll
  // placement runs in the layout effect of the same render that first has
  // turns, and has to see the boundary then.
  const decided = useRef<{ ref: string; boundary: SeenBoundary } | null>(null);
  if (decided.current?.ref !== ref) decided.current = null;
  if (decided.current === null && model && !isDormantTranscript(model.turns)) {
    decided.current = { ref, boundary: boundaryFor(ref, model) };
  }

  // What leaving this ref would record. Tracked per commit so the cleanup
  // below, which runs after the next ref's render, still reads the turns of
  // the ref it is closing.
  const latestSeen = useRef<{ ref: string; turnId: string } | null>(null);
  useEffect(() => {
    const turnId = model ? latestSettledTurnId(model) : undefined;
    if (turnId !== undefined) latestSeen.current = { ref, turnId };
  }, [ref, model]);

  useEffect(() => {
    return () => {
      const seen = latestSeen.current;
      if (seen?.ref === ref) writeSeenWatermark(ref, seen.turnId);
    };
  }, [ref]);

  return decided.current?.boundary ?? NO_BOUNDARY;
}

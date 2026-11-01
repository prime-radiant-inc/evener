import { useLayoutEffect, useMemo, useRef } from "react";
import type { TranscriptReader } from "../panes/session/transcript/useTranscript";
import {
  type ComicPositionRecord,
  type ComicReadingPosition,
  readComicPositionRecord,
  writeComicPosition,
} from "./readerPosition";

let nextReader = 0;
const retainedPositions = new WeakMap<object, Map<string, ComicPositionRecord>>();

/** Allocation is inert. Only the committed lifetime may request shared history. */
export function useComicReader(
  ref: string,
  seat?: object,
  sessionRef?: string,
): {
  reader: TranscriptReader;
  position: ComicReadingPosition | undefined;
  save(position: ComicReadingPosition): void;
} {
  const committedSession = useRef<{ ref: string; sessionRef: string } | undefined>(undefined);
  // A temporary missing model is not a new session. The durable/seat stamp
  // supplies the fence when Comic was unmounted during Clear.
  const knownSession =
    sessionRef ?? (committedSession.current?.ref === ref ? committedSession.current.sessionRef : undefined);
  const owner = useMemo(() => {
    let alive = false;
    const saved = (seat && retainedPositions.get(seat)?.get(ref)) || readComicPositionRecord(ref);
    const replaced = knownSession !== undefined && saved !== undefined && saved.ownerRef !== knownSession;
    const ownerRef = knownSession ?? saved?.ownerRef ?? ref;
    let position: ComicReadingPosition | undefined = replaced
      ? { mode: "end", sessionRef: ref, path: [] }
      : saved?.position;
    const subscribers = new Set<() => void>();
    const reader: TranscriptReader = {
      id: `comic-reader:${++nextReader}`,
      membership: "standalone",
      get alive() {
        return alive;
      },
      get readable() {
        return alive;
      },
      subscribe(listener) {
        subscribers.add(listener);
        return () => {
          subscribers.delete(listener);
        };
      },
    };
    return {
      replaced,
      reader,
      get position() {
        return position;
      },
      save(value: ComicReadingPosition) {
        if (!alive) return;
        position = value;
        if (seat) {
          let positions = retainedPositions.get(seat);
          if (!positions) {
            positions = new Map();
            retainedPositions.set(seat, positions);
          }
          positions.set(ref, { ownerRef, position: value });
        }
        writeComicPosition(ref, value, ownerRef);
      },
      setAlive(value: boolean) {
        alive = value;
        for (const listener of subscribers) listener();
      },
    };
  }, [ref, seat, knownSession]);
  useLayoutEffect(() => {
    owner.setAlive(true);
    if (knownSession !== undefined) committedSession.current = { ref, sessionRef: knownSession };
    if (owner.replaced && owner.position) owner.save(owner.position);
    return () => owner.setAlive(false);
  }, [owner, ref, knownSession]);
  return owner;
}

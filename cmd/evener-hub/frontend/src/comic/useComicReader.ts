import { useLayoutEffect, useMemo } from "react";
import type { TranscriptReader } from "../panes/session/transcript/useTranscript";
import { type ComicReadingPosition, readComicPosition, writeComicPosition } from "./readerPosition";

let nextReader = 0;
const retainedPositions = new WeakMap<object, Map<string, ComicReadingPosition>>();

/** Allocation is inert. Only the committed lifetime may request shared history. */
export function useComicReader(
  ref: string,
  seat?: object,
): {
  reader: TranscriptReader;
  position: ComicReadingPosition | undefined;
  save(position: ComicReadingPosition): void;
} {
  const owner = useMemo(() => {
    let alive = false;
    let position = (seat && retainedPositions.get(seat)?.get(ref)) || readComicPosition(ref);
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
          positions.set(ref, value);
        }
        writeComicPosition(ref, value);
      },
      setAlive(value: boolean) {
        alive = value;
        for (const listener of subscribers) listener();
      },
    };
  }, [ref, seat]);
  useLayoutEffect(() => {
    owner.setAlive(true);
    return () => owner.setAlive(false);
  }, [owner]);
  return owner;
}

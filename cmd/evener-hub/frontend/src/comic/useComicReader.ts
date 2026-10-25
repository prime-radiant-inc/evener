import { useLayoutEffect, useMemo } from "react";
import type { TranscriptReader } from "../panes/session/transcript/useTranscript";
import { type ComicReadingPosition, readComicPosition, writeComicPosition } from "./readerPosition";

let nextReader = 0;

/** Allocation is inert. Only the committed lifetime may request shared history. */
export function useComicReader(ref: string): {
  reader: TranscriptReader;
  position: ComicReadingPosition | undefined;
  save(position: ComicReadingPosition): void;
} {
  const owner = useMemo(() => {
    let alive = false;
    let position = readComicPosition(ref);
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
        writeComicPosition(ref, value);
      },
      setAlive(value: boolean) {
        alive = value;
        for (const listener of subscribers) listener();
      },
    };
  }, [ref]);
  useLayoutEffect(() => {
    owner.setAlive(true);
    return () => owner.setAlive(false);
  }, [owner]);
  return owner;
}

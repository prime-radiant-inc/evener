import { selectLocation } from "@evener/appwire-client/state/navigation";
import { useEffect, useState } from "react";
import { type TranscriptReader, useTranscriptReader } from "../panes/session/transcript/useTranscript";
import { useConnectionStore } from "../stores/connection";
import { navigationStore, useNavigationStore } from "../stores/navigation/store";
import { useThreadsStore } from "../stores/threads";
import { useThreadModel } from "../stores/useThreadModel";
import { Button } from "../widgets";
import { ComicPlainTranscript } from "./ComicPlainTranscript";
import { projectComicConversation } from "./projection";

let nextReader = 0;
function makeReader(): TranscriptReader & { dispose(): void } {
  const id = `comic-reader:${++nextReader}`;
  let alive = true;
  const subscribers = new Set<() => void>();
  return {
    id,
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
    dispose() {
      alive = false;
      for (const listener of subscribers) listener();
      subscribers.clear();
    },
  };
}

export function ComicSession({
  sessionRef,
  onReplaceRef,
  onSelectRef,
}: {
  sessionRef: string;
  onReplaceRef(ref: string): void;
  onSelectRef(ref: string): void;
}): React.ReactElement {
  const [reader] = useState(makeReader);
  const model = useThreadModel(sessionRef);
  const transcript = useTranscriptReader(sessionRef, reader);
  const deleted = useThreadsStore((state) => state.deletedRefs.has(sessionRef));
  const connection = useConnectionStore((state) => state.state);
  const location = useNavigationStore(selectLocation(sessionRef));
  useEffect(() => () => reader.dispose(), [reader]);
  useEffect(() => {
    if (connection !== "ready") return;
    void navigationStore
      .getState()
      .lookupLocation(sessionRef)
      .catch(() => {});
  }, [sessionRef, connection]);
  useEffect(() => {
    const resolvedRef = model?.ref;
    if (resolvedRef && resolvedRef !== sessionRef) onReplaceRef(resolvedRef);
  }, [model?.ref, sessionRef, onReplaceRef]);
  return (
    <section aria-label="Comic conversation">
      <header>
        <Button variant="link" onClick={() => onSelectRef("")}>
          Conversations
        </Button>
        <a href={`/s/${encodeURIComponent(sessionRef)}`}>Open in regular UI</a>
      </header>
      {!!location?.error && <div role="alert">Navigation location unavailable</div>}
      {deleted && <div role="alert">This conversation is no longer available</div>}
      {!model && !deleted && <p role="status">Loading conversation</p>}
      {model && (
        <>
          <ComicPlainTranscript entries={projectComicConversation(model)} />
          {transcript.hasOlder && (
            <Button variant="link" disabled={transcript.loadingOlder} onClick={transcript.loadOlderReportingError}>
              Older messages
            </Button>
          )}
          {transcript.olderError && (
            <div role="alert">
              {transcript.olderError}{" "}
              <Button variant="link" onClick={transcript.loadOlderReportingError}>
                Retry
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  );
}

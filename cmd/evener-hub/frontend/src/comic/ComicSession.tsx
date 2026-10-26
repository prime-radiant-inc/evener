import type { ThreadModel } from "@evener/appwire-client";
import { selectLocation } from "@evener/appwire-client/state/navigation";
import { useEffect, useId, useMemo, useState } from "react";
import { useTranscriptReader } from "../panes/session/transcript/useTranscript";
import { useConnectionStore } from "../stores/connection";
import { navigationStore, useNavigationStore } from "../stores/navigation/store";
import { useThreadsStore } from "../stores/threads";
import { useThreadModel } from "../stores/useThreadModel";
import { Button, Select } from "../widgets";
import { ComicTranscript } from "./ComicTranscript";
import styles from "./comic.module.css";
import { projectComicConversation } from "./projection";
import { useComicActivity } from "./useComicActivity";
import { useComicReader } from "./useComicReader";
import { useComicSubchats } from "./useComicSubchats";

export function ComicSession({
  sessionRef,
  onReplaceRef,
  onSelectRef,
}: {
  sessionRef: string;
  onReplaceRef(ref: string): void;
  onSelectRef(ref: string): void;
}): React.ReactElement {
  const reader = useComicReader(sessionRef);
  const viewId = useId();
  const [mode, setMode] = useState<"comic" | "plain">("comic");
  const model = useThreadModel(sessionRef);
  const transcript = useTranscriptReader(sessionRef, reader.reader);
  const deleted = useThreadsStore((state) => state.deletedRefs.has(sessionRef));
  const connection = useConnectionStore((state) => state.state);
  const location = useNavigationStore(selectLocation(sessionRef));
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
    <section className={styles.session} aria-label="Comic conversation">
      <header>
        <Button variant="link" onClick={() => onSelectRef("")}>
          Conversations
        </Button>
        <a href={`/s/${encodeURIComponent(sessionRef)}`}>Open in regular UI</a>
        <label htmlFor={viewId}>
          View
          <Select
            id={viewId}
            value={mode}
            options={[
              { value: "comic", label: "Comic" },
              { value: "plain", label: "Plain" },
            ]}
            onChange={(event) => setMode(event.target.value as "comic" | "plain")}
          />
        </label>
      </header>
      {!!location?.error && <div role="alert">Navigation location unavailable</div>}
      {deleted && <div role="alert">This conversation is no longer available</div>}
      {!model && !deleted && <p role="status">Loading conversation</p>}
      {model && (
        <ComicSessionContent
          key={sessionRef}
          sessionRef={sessionRef}
          model={model}
          history={transcript}
          reader={reader}
          mode={mode}
          onOpen={onSelectRef}
        />
      )}
    </section>
  );
}

function ComicSessionContent({
  sessionRef,
  model,
  history,
  reader,
  mode,
  onOpen,
}: {
  sessionRef: string;
  model: ThreadModel;
  history: ReturnType<typeof useTranscriptReader>;
  reader: ReturnType<typeof useComicReader>;
  mode: "comic" | "plain";
  onOpen(ref: string): void;
}) {
  const activity = useComicActivity(sessionRef, model);
  const entries = useMemo(() => projectComicConversation(model, activity.context), [model, activity.context]);
  const subchats = useComicSubchats(sessionRef, reader.position);
  const root = useMemo(
    () => ({ ref: sessionRef, path: [], entries, history, activity }),
    [sessionRef, entries, history, activity],
  );
  return (
    <>
      {subchats.lifetimes}
      <ComicTranscript root={root} subchats={subchats.state} reader={reader} mode={mode} onOpen={onOpen} />
    </>
  );
}

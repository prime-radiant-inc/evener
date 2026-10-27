import type { ThreadModel } from "@evener/appwire-client";
import { useMemo } from "react";
import type { useTranscriptReader } from "../panes/session/transcript/useTranscript";
import { ComicTranscript } from "./ComicTranscript";
import { projectComicConversation } from "./projection";
import { useComicActivity } from "./useComicActivity";
import type { useComicReader } from "./useComicReader";
import { useComicSubchats } from "./useComicSubchats";

export function ComicSessionContent({
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

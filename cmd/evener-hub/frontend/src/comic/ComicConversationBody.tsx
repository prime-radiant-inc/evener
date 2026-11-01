import { useEffect, useId, useState } from "react";
import type { ConversationParts, ConversationSeat } from "../panes/session/ConversationBody";
import { SessionChrome } from "../panes/session/chrome/SessionChrome";
import { NOW_TICK_MS, useNowTick } from "../panes/session/liveness";
import { PendingChips } from "../panes/session/pending/PendingChips";
import { LivenessLine } from "../panes/session/transcript/flow/LivenessLine";
import { useTranscriptReader } from "../panes/session/transcript/useTranscript";
import { navigate, paneToURL } from "../shell/routing";
import { cadenceStateFor } from "../shell/sessionStatus";
import { useIsMobile } from "../shell/useIsMobile";
import { useThreadsStore } from "../stores/threads";
import { TranscriptRenderProvider } from "../transcriptDisplay/renderContext";
import { Cadence, InlineNotice, Select } from "../widgets";
import { ComicComposer } from "./ComicComposer";
import { ComicInteractions } from "./ComicInteractions";
import { ComicSessionContent } from "./ComicSession";
import styles from "./comic.module.css";
import { comicSessionRef } from "./comicSessionIdentity";
import { loadComicFonts } from "./measure";
import { useComicReader } from "./useComicReader";

/** Presentation only: the workspace seat owns the source, claim and resumed identity. */
export function ComicConversationBody({
  ref,
  seat,
  render,
}: {
  ref: string;
  seat: ConversationSeat;
  render(parts: ConversationParts): React.ReactNode;
}) {
  const model = useThreadsStore((state) => state.threads.get(ref));
  const reader = useComicReader(
    ref,
    seat.readView ?? seat.composerSource ?? undefined,
    model && comicSessionRef(model),
  );
  const history = useTranscriptReader(ref, reader.reader);
  const deleted = useThreadsStore((state) => state.deletedRefs.has(ref));
  const frames = useThreadsStore((state) => state.frameTimes.get(ref) ?? EMPTY_FRAMES);
  const now = useNowTick(NOW_TICK_MS);
  const isMobile = useIsMobile();
  const [mode, setMode] = useState<"comic" | "plain">("comic");
  const viewId = useId();
  const [fontStatus, setFontStatus] = useState<"loading" | "ready" | "unavailable">("loading");
  useEffect(() => {
    let active = true;
    void loadComicFonts().then((status) => {
      if (active) setFontStatus(status);
    });
    return () => {
      active = false;
    };
  }, []);
  const liveness = model && (
    <LivenessLine
      lastFrameAt={model.lastFrameAt}
      now={now}
      active={model.status.type === "active"}
      sessionRef={ref}
      turnId={model.runningTurnId ?? model.activeTurnId}
      retry={model.modelRetry}
      primaryModel={model.model}
    />
  );
  const parts: ConversationParts =
    deleted || !model
      ? {
          state: deleted ? "deleted" : "loading",
          content: null,
          footer: null,
          cadence: null,
        }
      : {
          state: "live",
          content: (
            <div className={styles.paneBody}>
              {fontStatus === "unavailable" && (
                <InlineNotice live="status" aria-label="Comic font unavailable">
                  {isMobile
                    ? "Comic Sans unavailable, using fallback font."
                    : "Comic Sans could not load. The conversation remains available in the fallback font."}
                </InlineNotice>
              )}
              <TranscriptRenderProvider sourcePaneId={seat.viewFallbackId}>
                <ComicSessionContent
                  sessionRef={ref}
                  model={model}
                  history={history}
                  reader={reader}
                  keyboardScrollPaneId={seat.keyboardScrollPaneId}
                  mode={mode}
                  appearanceView={
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
                  }
                  onOpen={(target) => {
                    const url = paneToURL("session", { ref: target });
                    if (url !== null) navigate(url);
                  }}
                />
              </TranscriptRenderProvider>
              <ComicInteractions sessionRef={ref} paneId={seat.viewFallbackId} />
            </div>
          ),
          footer: (
            <>
              {(!isMobile || !seat.composerSource) && liveness}
              <PendingChips sessionRef={ref} />
              {seat.composerSource && (
                <ComicComposer
                  source={seat.composerSource}
                  paneId={seat.viewFallbackId}
                  headingStatus={isMobile ? liveness : undefined}
                  sessionActions={
                    isMobile && seat.sessionMenu ? (
                      <SessionChrome ref={ref} paneId={seat.viewFallbackId} placement="menu" />
                    ) : undefined
                  }
                />
              )}
              {seat.sessionMenu && (!isMobile || !seat.composerSource) && (
                <SessionChrome ref={ref} paneId={seat.viewFallbackId} placement="menu" />
              )}
            </>
          ),
          cadence: <Cadence state={cadenceStateFor(model.status.type)} frameTimes={frames} now={now} />,
        };
  return (
    <section className={styles.comicRoot} data-theme="light" data-comic-font={fontStatus} data-session-ref={ref}>
      {render(parts)}
    </section>
  );
}

const EMPTY_FRAMES: number[] = [];

import { hydrateThread, makeTranscriptDisplayConfig, type ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { useEffect, useMemo, useState } from "react";
import { Composer } from "../panes/session/composer/Composer";
import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import { TranscriptBody } from "../panes/session/transcript/TranscriptBody";
import { ClientProvider } from "../shell/clientContext";
import { putThreadModel } from "../stores/threads";
import { ALL_CAPABILITIES, testThread } from "../threadTestUtils";
import { PaneScaffold, SegmentedControl } from "../widgets";
import styles from "./composer-responsive.module.css";
import { makeEditorialTranscriptModel } from "./surface-sections/transcript-fixture";

const DEMO_DRAFT = "Keep the composer compact until this draft arrives, then let the full controls return.";
const TRANSCRIPT_CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });

type FrameName = "phone" | "tablet" | "desktop";
type DraftMode = "empty" | "draft";

const FRAMES: readonly { id: FrameName; label: string; width: number }[] = [
  { id: "phone", label: "Phone", width: 390 },
  { id: "tablet", label: "Tablet", width: 768 },
  { id: "desktop", label: "Desktop", width: 1180 },
];

function ResponsiveSessionPreview({ frame, draft }: { frame: FrameName; draft: boolean }) {
  const ref = `dev-composer-responsive-${frame}`;
  const [client] = useState(() => new FakeClient("ready"));
  const [source, setSource] = useState<ComposerSourceState | null>(null);
  const transcript = useMemo(() => makeEditorialTranscriptModel(), []);

  useEffect(() => {
    const thread = testThread(ref, {
      status: { type: "active" },
      evener: { ref, capabilities: ALL_CAPABILITIES, activeTurnId: "turn_demo", queue: { revision: 0 } },
    });
    putThreadModel(ref, hydrateThread({ thread } as ThreadReadResponse, ref, Date.now()));
    const owner = createComposerSourceState(ref);
    if (draft) owner.writeDraft(DEMO_DRAFT, false);
    setSource(owner);
    return () => owner.dispose();
  }, [draft, ref]);

  return (
    <ClientProvider client={client}>
      <div className={styles.session}>
        <PaneScaffold
          title="A quieter session footer"
          scaffoldMarker={`session:${ref}`}
          footer={source ? <Composer ref={ref} source={source} focused /> : undefined}
        >
          <TranscriptBody
            model={transcript}
            config={TRANSCRIPT_CONFIG}
            surface="preview"
            disclosureScope={`preview:${ref}`}
            sessionRef={ref}
          />
        </PaneScaffold>
      </div>
    </ClientProvider>
  );
}

function ComparisonPage() {
  const [mode, setMode] = useState<DraftMode>("empty");
  return (
    <main className={styles.page}>
      <header className={styles.intro}>
        <h1>Responsive composer</h1>
        <p>
          Focus leaves an empty composer folded. Typing brings back the full writing surface. Each preview is the real
          transcript and composer in its own viewport.
        </p>
        <SegmentedControl<DraftMode>
          label="Composer content"
          value={mode}
          options={[
            { value: "empty", label: "Empty" },
            { value: "draft", label: "With a draft" },
          ]}
          onChange={setMode}
          size="sm"
        />
      </header>
      <div className={styles.previews}>
        {FRAMES.map(({ id, label, width }) => (
          <section className={styles.preview} key={id}>
            <div className={styles.previewHeading}>
              <h2>{label}</h2>
              <p>{width}px viewport</p>
            </div>
            <div className={`${styles.frame} ${styles[id]}`}>
              <iframe
                title={`${label} session preview`}
                data-frame={id}
                src={`/dev/composer-responsive?frame=${id}&draft=${mode === "draft" ? 1 : 0}`}
              />
            </div>
          </section>
        ))}
      </div>
    </main>
  );
}

export default function ComposerResponsiveDemo() {
  const params = new URLSearchParams(window.location.search);
  const frame = FRAMES.find((candidate) => candidate.id === params.get("frame"))?.id;
  if (frame) return <ResponsiveSessionPreview frame={frame} draft={params.get("draft") === "1"} />;
  return <ComparisonPage />;
}

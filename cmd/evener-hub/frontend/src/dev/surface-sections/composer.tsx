// The composer surface: the REAL Composer component (panes/session/
// composer/Composer.tsx), fed by seeding threadsStore directly with a
// hydrated ThreadModel - the same hydrateThread() the real read/snapshot
// path uses, just given fixture wire data instead of a network response.
// No network: threadsStore is a plain zustand store, seeded via setState the
// same way this app's own tests seed it (see Composer.test.tsx's
// testThread/readResponse helpers, which this mirrors).
//
// AskDock's pending-question card is NOT set by hand: askDockStore
// reconciles itself off threadsStore changes (askDockStore.ts's own
// threadsStore.subscribe wiring), so seeding a thread whose transcript ends
// on an unanswered ask_user call is what makes the real dock populate -
// exactly the mechanism a live ask_user call would use.
import { useEffect } from "react";
import { Composer } from "../../panes/session/composer/Composer";
import { writeDraft } from "../../panes/session/composer/draft";
import { hydrateThread } from "../../protocol/reducer";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { threadsStore } from "../../stores/threads";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function fixtureThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "dev fixture",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/home/dev/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

const RESTING_REF = "dev-surface-composer-resting";
const DRAFTED_REF = "dev-surface-composer-drafted";
const ASK_REF = "dev-surface-composer-ask";

// AskDock reconciles from live ask_user questions in the transcript
// (deriveAskQuestions.ts's liveAskQuestions): a completed, unanswered
// ask_user call after the last user message is "live". This is the exact
// wire shape a real ask_user call produces.
const askThread: Thread = fixtureThread(ASK_REF, {
  turns: [
    {
      id: "turn_1",
      status: "completed",
      itemsView: "full",
      items: [
        { type: "userMessage", id: "u1", turnId: "turn_1", text: "Ship the prune fix?" },
        {
          type: "commandExecution",
          id: "ask1",
          turnId: "turn_1",
          toolName: "ask_user",
          callId: "call_ask_1",
          status: "completed",
          argumentsJson: JSON.stringify({
            questions: [
              {
                header: "Backport?",
                question: "Should this fix also land on the release/1.4 branch?",
                options: [
                  { label: "Yes, backport", detail: "Cherry-pick once main is green.", recommended: true },
                  { label: "No, main only", detail: "The release branch doesn't hit this path." },
                ],
              },
            ],
          }),
        },
      ],
    },
  ],
});

function seedComposerFixtures(): void {
  const now = Date.now();
  const next = new Map(threadsStore.getState().threads);
  next.set(RESTING_REF, hydrateThread({ thread: fixtureThread(RESTING_REF) } as ThreadReadResponse, RESTING_REF, now));
  next.set(DRAFTED_REF, hydrateThread({ thread: fixtureThread(DRAFTED_REF) } as ThreadReadResponse, DRAFTED_REF, now));
  next.set(ASK_REF, hydrateThread({ thread: askThread } as ThreadReadResponse, ASK_REF, now));
  threadsStore.setState({ threads: next });
  writeDraft(DRAFTED_REF, "One more thing - can you also add a CHANGELOG entry for this?");
}

export default function ComposerSurfaceSection() {
  useEffect(() => {
    seedComposerFixtures();
  }, []);

  return (
    <section>
      <h2>Composer</h2>
      <p className={styles.note}>
        Real Composer, fed by a threadsStore seeded directly (hydrateThread over fixture wire data - no network).
        Resting, with drafted text, and with a pending ask_user question (AskDock reconciles itself off the seeded
        thread's transcript, the same way it would off a live ask_user call).
      </p>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>resting</p>
          <Composer ref={RESTING_REF} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>drafted</p>
          <Composer ref={DRAFTED_REF} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>ask pending</p>
          <Composer ref={ASK_REF} />
        </div>
      </ThemeFlip>
    </section>
  );
}

import { TranscriptBody } from "../../panes/session/transcript/TranscriptBody";
import { makeTranscriptDisplayConfig } from "../../transcriptDisplay/config";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";
import { makeEditorialTranscriptModel } from "./transcript-fixture";

const SESSION_REF = "dev-surface-transcript";
const PREVIEW_CONFIG = makeTranscriptDisplayConfig(
  { kind: "preset", level: "full" },
  {
    roundTimings: true,
    tokenCounts: true,
    estimatedCost: true,
    systemEvents: true,
    promptEvents: true,
    hookExits: "all",
  },
);

/**
 * The gallery uses the same fixed, production-shaped fixture as Settings. It
 * therefore exercises the real projector and TranscriptBody without stores,
 * network access, live clocks, or copied renderer markup.
 */
export default function TranscriptSurfaceSection() {
  return (
    <section>
      <h2>Transcript</h2>
      <p className={styles.note}>
        A fixed user request, successful and failed tool calls, reasoning, an agent response, and optional metrics and
        system events, plus durable collaborators with attention, failure, exhaustion, and unavailable lifecycle data.
        Rendered through the production TranscriptBody preview surface with no store or network.
      </p>
      <ThemeFlip>
        <TranscriptBody
          model={makeEditorialTranscriptModel()}
          config={PREVIEW_CONFIG}
          surface="preview"
          disclosureScope="preview:dev-transcript"
          sessionRef={SESSION_REF}
        />
      </ThemeFlip>
    </section>
  );
}

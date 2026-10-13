import { type ItemModel, makeTranscriptDisplayConfig, type ThreadModel } from "@evener/appwire-client";
import { TranscriptBody } from "../../panes/session/transcript/TranscriptBody";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const TURN_ID = "disclosure-turn";
const SESSION_REF = "dev-surface-transcript-disclosures";

const SHELL_NOTIFICATION = `<job-notification job_id="job_build" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="42" exit_code="0">
Job job_build completed.
excerpt:
built 14 packages in 3.2s
</job-notification>`;

const DELEGATE_NOTIFICATION = `<delegate-notification delegate_id="dlg_audit" event="completed" status="completed" transcript_ref="local:ref_audit">
Delegate dlg_audit completed.
message:
The audit found no regressions in the transcript disclosures.
</delegate-notification>`;

function item(id: string, type: string, text: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: TURN_ID, type, text, status: "completed", ...overrides };
}

function systemItem(id: string, eventKind: ItemModel["eventKind"], text: string): ItemModel {
  return item(id, "systemMessage", text, { eventKind });
}

/** Every transcript row that folds behind a disclosure, in one turn: the
 * intent group, a thought, a steering divider, both notification card heads,
 * a system-prompt scaffold, and a grouped run of system events. */
function makeDisclosureModel(): ThreadModel {
  const model = makeTranscriptPreviewModel();
  model.turns = [
    {
      id: TURN_ID,
      status: "completed",
      items: [
        item("disclosure-user", "userMessage", "Check every folded transcript row"),
        item("disclosure-read", "commandExecution", "", {
          toolName: "read_file",
          description: "Reading the transcript renderer",
          argumentsJSON: '{"path":"src"}',
          output: "src/panes/session/transcript",
          exitCode: 0,
        }),
        item("disclosure-grep", "commandExecution", "", {
          toolName: "shell",
          description: "Listing every disclosure call site",
          argumentsJSON: '{"command":"rg details"}',
          output: "6 matches",
          exitCode: 0,
        }),
        item("disclosure-reasoning", "reasoning", "", {
          reasoningSummaries: [
            ["Each folded row should read like every other disclosure in the app."],
            ["The summary names the row; the body holds the detail."],
          ],
        }),
        item("disclosure-steering", "steering", "Focus on the transcript rows before the settings pane."),
        item("disclosure-notifications", "steering", `${SHELL_NOTIFICATION}\n${DELEGATE_NOTIFICATION}`, {
          steeringKind: "notification",
        }),
        systemItem(
          "disclosure-system-prompt",
          "system_prompt",
          "You are a careful assistant.\n\nRead before you edit.",
        ),
        item("disclosure-agent", "agentMessage", "Every folded row is accounted for."),
        item("disclosure-model", "systemMessage", "Model switched to the preview model."),
        item("disclosure-skill", "systemMessage", "Skill activated: transcript review."),
        item("disclosure-plugin", "systemMessage", "Plugin loaded: preview tools."),
      ],
    },
  ];
  return model;
}

function config(expandByDefault: boolean) {
  return makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault },
    { systemEvents: true, promptEvents: true },
  );
}

/**
 * The transcript's folded rows, collapsed and expanded, through the
 * production TranscriptBody preview surface with no store or network.
 */
export default function TranscriptDisclosuresSurfaceSection() {
  return (
    <section data-testid="transcript-disclosures-surface">
      <h2>Transcript disclosures</h2>
      <p className={styles.note}>
        Every transcript row that folds behind a disclosure: the intent group, a thought, a steering divider, job and
        delegate notification cards, the system prompt, and a grouped run of system events. Collapsed first, then
        expanded.
      </p>
      <ThemeFlip>
        <TranscriptBody
          model={makeDisclosureModel()}
          config={config(false)}
          surface="preview"
          disclosureScope="preview:dev-transcript-disclosures-collapsed"
          sessionRef={`${SESSION_REF}-collapsed`}
        />
        <TranscriptBody
          model={makeDisclosureModel()}
          config={config(true)}
          surface="preview"
          disclosureScope="preview:dev-transcript-disclosures-expanded"
          sessionRef={`${SESSION_REF}-expanded`}
        />
      </ThemeFlip>
    </section>
  );
}

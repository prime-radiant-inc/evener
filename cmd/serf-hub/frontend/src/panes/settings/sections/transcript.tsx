import { prefsStore, type TranscriptStatusKey, usePrefsStore } from "../../../stores/prefs";
import { Switch, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./transcript.module.css";

const CLASS = {
  root: requireClass(styles.root, "transcript.module.css", "root"),
  intro: requireClass(styles.intro, "transcript.module.css", "intro"),
  row: requireClass(styles.row, "transcript.module.css", "row"),
  help: requireClass(styles.help, "transcript.module.css", "help"),
};

interface ToggleSpec {
  key: TranscriptStatusKey;
  label: string;
  help: string;
}

const TOGGLES: ToggleSpec[] = [
  { key: "roundTimings", label: "Round timings", help: "Show how long each round took, under the round." },
  {
    key: "tokenCounts",
    label: "Token counts",
    help: "Show each round's input and output token counts, under the round.",
  },
  { key: "hookExitsAll", label: "Hook exits (all)", help: "Show every hook exit line, including non-zero exits." },
  {
    key: "hookExitsNormal",
    label: "Hook exits (normal only)",
    help: "Show only hook exit lines with exit code 0. The all-hooks setting includes these too.",
  },
  {
    // Sentence case: the legacy's own copy is "Prompt Loaded" (Title Case),
    // inconsistent with the other 3 labels in this same section - normalized
    // per this wave's sentence-case gate (see the wave-7 report).
    key: "promptLoaded",
    label: "Prompt loaded",
    // Names both item kinds this one toggle governs (system_prompt and
    // prompt_loaded - see transcriptVisibility.ts): the old copy described
    // only the disclosure, leaving the notices an unannounced surprise.
    help: "Show the session's system prompt as an expandable disclosure, and a notice for each prompt loaded.",
  },
];

/**
 * Settings -> Transcript (parity-m7-settings.md §4): independent
 * boolean toggles, all default off, localStorage-only (no wire access).
 * Rendered from one spec array rather than near-identical blocks - the
 * legacy's checkbox handlers are otherwise byte-identical modulo their
 * key/label/help, which prefs.ts's own setTranscriptStatus(key, value)
 * already generalizes on the store side.
 */
export function TranscriptSection() {
  const transcript = usePrefsStore((s) => s.transcript);
  const { push } = useToasts();

  function handleChange(key: TranscriptStatusKey, value: boolean) {
    prefsStore.getState().setTranscriptStatus(key, value);
    push("success", "Settings saved");
  }

  return (
    <div className={CLASS.root}>
      {/* Not "System status items ...": that framing dates from when all four
          toggles gated the legacy's systemStatus blob, and it misdescribes
          Round timings and Token counts, which annotate turns rather than
          show a system event. What every row here has in common is that it
          adds optional detail to the transcript. */}
      <p className={CLASS.intro}>
        Optional transcript detail. Each browser keeps its own settings.
      </p>
      {TOGGLES.map((toggle) => (
        <div key={toggle.key} className={CLASS.row}>
          <Switch
            label={toggle.label}
            checked={transcript[toggle.key]}
            onChange={(value) => handleChange(toggle.key, value)}
          />
          <p className={CLASS.help}>{toggle.help}</p>
        </div>
      ))}
    </div>
  );
}

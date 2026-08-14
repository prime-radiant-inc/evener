import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { type TreeNode, useTreeStore } from "../../stores/tree";
import { Button, EmptyState, KeyHint, PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./welcome.module.css";

const CLASS = {
  actions: requireClass(styles.actions, "welcome.module.css", "actions"),
  hints: requireClass(styles.hints, "welcome.module.css", "hints"),
  hintRow: requireClass(styles.hintRow, "welcome.module.css", "hintRow"),
  hintFooter: requireClass(styles.hintFooter, "welcome.module.css", "hintFooter"),
};

// T6: the chords a new person has no other way to discover from this cold
// pane - CommandPalette.tsx's own HELP_ROWS is the source of truth for all
// three (Mod+K/Mod+I/Mod+J); order matches HELP_ROWS' own listing.
const CHORD_HINTS: { keys: string[]; desc: string }[] = [
  { keys: ["Mod", "K"], desc: "command palette" },
  { keys: ["Mod", "I"], desc: "focus the composer" },
  { keys: ["Mod", "J"], desc: "next session needing you" },
];

const EXAMPLE_PROMPTS = [
  "Find and fix the root cause of a flaky test",
  "Audit error handling across the auth package",
  "Explain how requests flow from router to handler",
] as const;

export interface WelcomePaneParams {
  // Shown as the empty state's hint - e.g. the note /new renders while the
  // spawn pane doesn't exist yet (Wave 6). Absent on the plain "/" welcome.
  note?: string;
}

function goToNewSession(): void {
  const url = paneToURL("spawn", {});
  if (url) navigate(url);
}

function goToExample(prompt: string): void {
  const url = paneToURL("spawn", {});
  if (url) navigate(`${url}?prompt=${encodeURIComponent(prompt)}`);
}

function goToSession(ref: string): void {
  const url = paneToURL("session", { ref });
  if (url) navigate(url);
}

/** tbk8: the one session this cold pane offers to resume, when there is one.
 * A rail beside this pane may ALSO show the same session (it's always
 * docked on desktop as of the rail redesign) - that overlap is fine, and is
 * what keeps this pane itself correct on a narrow viewport, where there is
 * no rail to fall back on, and on a genuinely cold load with no restored
 * pane layout to have opened it already.
 *
 * needs-you outranks live, matching the rail's own attention ordering
 * (railNodes.ts's sessionWantsYou / hubapi.AttentionRank): a session
 * blocked on you is a stronger "come back" signal than one merely still
 * running. Only the first candidate in whichever tier is non-empty - this
 * is a single "jump back in" link, not a list; a list is the rail's job. */
function resumeCandidate(tree: { needs_you: TreeNode[]; live: TreeNode[] } | null): TreeNode | undefined {
  return tree?.needs_you[0] ?? tree?.live[0];
}

export default function Welcome({ params }: PaneProps<WelcomePaneParams>) {
  const tree = useTreeStore((s) => s.tree);
  const candidate = resumeCandidate(tree);

  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        hint={params.note}
        action={
          <div className={CLASS.actions}>
            {candidate !== undefined && (
              <Button variant="primary" onClick={() => goToSession(candidate.ref)}>
                Jump back in: {candidate.title}
                {candidate.age !== undefined && candidate.age !== "" ? ` · ${candidate.age}` : ""}
              </Button>
            )}
            <Button variant="quiet" onClick={goToNewSession}>
              New session
            </Button>
            <p className={styles.orientation}>
              A session can read and edit the repository, run commands, and delegate work to helpers.
            </p>
            <div className={styles.examples}>
              <p className={styles.examplesLabel}>Try a task to get started</p>
              {EXAMPLE_PROMPTS.map((prompt) => (
                <Button key={prompt} variant="quiet" size="sm" onClick={() => goToExample(prompt)}>
                  {prompt}
                </Button>
              ))}
            </div>
            <div className={CLASS.hints}>
              {CHORD_HINTS.map((hint) => (
                <div className={CLASS.hintRow} key={hint.desc}>
                  <KeyHint keys={hint.keys} />
                  <span>{hint.desc}</span>
                </div>
              ))}
              <p className={CLASS.hintFooter}>
                <KeyHint keys={["?"]} /> inside the command palette shows all shortcuts.
              </p>
            </div>
          </div>
        }
      />
    </PaneScaffold>
  );
}

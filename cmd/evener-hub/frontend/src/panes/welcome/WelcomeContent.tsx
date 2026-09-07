import { navigate, paneToURL } from "../../shell/routing";
import { selectLiveRows, selectNeedsYouRows } from "../../stores/navigation/selectors";
import { type NavigationStoreState, useNavigationStore } from "../../stores/navigation/store";
import { Button, KeyHint } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./welcome.module.css";

const CLASS = {
  actions: requireClass(styles.actions, "welcome.module.css", "actions"),
  hints: requireClass(styles.hints, "welcome.module.css", "hints"),
  hintList: requireClass(styles.hintList, "welcome.module.css", "hintList"),
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

export interface WelcomeContentProps {
  note?: string;
  showNewSession?: boolean;
  showResume?: boolean;
  showHints?: boolean;
}

function goToNewSession(): void {
  const url = paneToURL("spawn", {});
  if (url) navigate(url);
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
function resumeCandidate(navigation: NavigationStoreState) {
  return selectNeedsYouRows(navigation)[0] ?? selectLiveRows(navigation)[0];
}

/**
 * Presentational body of the Welcome pane: the optional "Jump back in"
 * resume candidate, an optional "New session" action, orientation text, and
 * optional chord hints. No example prompts, and no host-conditional
 * ("am I mobile?") behavior - this component renders exactly what its
 * props ask for regardless of the viewport. The host decides which of these
 * to show.
 */
export function WelcomeContent({ note, showNewSession, showResume = true, showHints }: WelcomeContentProps) {
  const navigation = useNavigationStore();
  const candidate = resumeCandidate(navigation);

  return (
    <div className={CLASS.actions}>
      {note && <p>{note}</p>}
      {showResume && candidate !== undefined && (
        <Button variant="primary" onClick={() => goToSession(candidate.ref)}>
          Jump back in: {candidate.title}
        </Button>
      )}
      {showNewSession && (
        <Button variant="secondary" onClick={goToNewSession}>
          New session
        </Button>
      )}
      <p className={styles.orientation}>
        A session can read and edit the repository, run commands, and delegate work to helpers.
      </p>
      {showHints && (
        <div className={CLASS.hints}>
          <dl className={CLASS.hintList}>
            {CHORD_HINTS.map((hint) => (
              <div className={CLASS.hintRow} key={hint.desc}>
                <dt>
                  <KeyHint keys={hint.keys} />
                </dt>
                <dd>{hint.desc}</dd>
              </div>
            ))}
          </dl>
          <p className={CLASS.hintFooter}>
            <KeyHint keys={["?"]} /> inside the command palette shows all shortcuts.
          </p>
        </div>
      )}
    </div>
  );
}

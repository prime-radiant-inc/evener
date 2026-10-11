// taskcardmockups.html's entry: the inline task-list card rework mock-ups.
// Five variants (plus today's card as a baseline) render through the
// PRODUCTION TranscriptBody pipeline - mock descriptors (variants.tsx)
// consume the same wire-shaped fixtures (fixture.ts) the real card does, so
// the row grammar, two-level disclosure, fold interaction, and theming are
// all the real thing. Each variant demonstrates the rework ruleset: folded
// shows only the most recent update; the open body shows at most three
// tasks (most-recently-settled, in progress, next); a note renders only
// when this call added it.
//
// Dev-support scaffolding for a design decision, not production code.

import { makeTranscriptDisplayConfig, type ThreadModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { TasksPanelBody } from "../panes/session/chrome/TasksPanel";
// Registers the session panel pane types: the cards' Open-list buttons call
// openPane("sessionTasks"), which throws on an unregistered type - without
// this side-effect import the standalone preview's buttons are landmines.
import "../panes/sessionPanels";
import { TranscriptBody } from "../panes/session/transcript/TranscriptBody";
import { connectionStore } from "../stores/connection";
import { toggleDisclosure } from "../widgets/disclosure/disclosureStore";
import { requireClass } from "../widgets/internal/requireClass";
import "../styles/tokens.css";
import "../styles/global.css";
// Named galleryStyles, not gallery: the requireclass-contract scanner binds
// member accesses per identifier, and an import named `gallery` would make
// the file-hint string "gallery.module.css" read as `gallery.module` - a
// "missing class" that exists only in the hint.
import galleryStyles from "./gallery.module.css";
import sectionStyles from "./gallery-section.module.css";
import { ThemeFlip } from "./ThemeFlip";
import {
  ALLDONE,
  APPEND,
  CANCELLED,
  MAIN,
  MAIN_NOFRESH,
  modelForSection,
  readItem,
  type Scenario,
  SMALL,
  scenarioItem,
} from "./taskcardmockups/fixture";
// Registers the mock_taskcard_a..e descriptors (side-effect import).
import "./taskcardmockups/variants";

const GALLERY = requireClass(galleryStyles.gallery, "gallery.module.css", "gallery");
const INTRO = requireClass(galleryStyles.intro, "gallery.module.css", "intro");
const NOTE = requireClass(sectionStyles.note, "gallery-section.module.css", "note");

// toolCalls true (summary lines visible) with expandByDefault false: the
// "full" preset force-expands every body, which would defeat the folded
// mock - each item's folded/open state must be exactly what its descriptor's
// autoExpand picks.
const CONFIG = makeTranscriptDisplayConfig({
  kind: "custom",
  toolIntent: true,
  toolCalls: true,
  reasoning: false,
  expandByDefault: false,
});

const SESSION_REF = "dev-taskcardmockups";

function Section({ title, blurb, children }: { title: string; blurb: string; children: ReactNode }) {
  return (
    <section>
      <h2>{title}</h2>
      <p className={NOTE}>{blurb}</p>
      <ThemeFlip>{children}</ThemeFlip>
    </section>
  );
}

function TaskTranscript({ scope, model }: { scope: string; model: ThreadModel }) {
  return (
    <TranscriptBody model={model} config={CONFIG} surface="preview" disclosureScope={scope} sessionRef={SESSION_REF} />
  );
}

// The production card in a realistic run: the reads around it fold into an
// "N actions" header exactly as they do live.
const todayModel = modelForSection("preview:taskcardmockups-today", {
  id: "turn-today",
  userText:
    "We need to rework the inline task list display. Folded should show only the most recent update, unfolded should show at most three tasks, and notes only when just added.",
  items: [
    readItem(
      "today-read-1",
      "turn-today",
      "cmd/evener-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx",
      "Reading the current card's renderer before reworking it",
      "   1\t// The task_list descriptor: renders a task-update card for a successful\n   2\t// append/update mutation (parity-m4 §9). A view renders nothing; a failed\n   3\t// mutation renders no card either.\n",
    ),
    readItem(
      "today-read-2",
      "turn-today",
      "cmd/evener-hub/frontend/src/panes/session/transcript/tools/taskData.ts",
      "Reading the state parser the card reuses",
      "   1\texport function parseTaskState(raw: unknown): TaskRow[] | null {\n   2\t  return parseTaskListData(raw);\n   3\t}\n",
    ),
    scenarioItem("today-card", "task_list", MAIN, "turn-today"),
    readItem(
      "today-read-3",
      "turn-today",
      "cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx",
      "Checking the harness pattern for rendering real components",
      "   1\t// Renders the REAL shell with a fixture client: production\n   2\t// components, production theming, canned data.\n",
    ),
  ],
});

// One variant's section model: the same scenario folded, open (with the
// fresh note), and - for the ladder - open again without any fresh note.
function variantModel(
  scope: string,
  turnId: string,
  userText: string,
  toolName: string,
  scenarios: { id: string; scenario: Scenario }[],
): ThreadModel {
  return modelForSection(scope, {
    id: turnId,
    userText,
    items: scenarios.map(({ id, scenario }) => scenarioItem(id, toolName, scenario, turnId)),
  });
}

const ladderModel = variantModel(
  "preview:taskcardmockups-a",
  "turn-a",
  "Show the ladder folded, open with a fresh note, and open with none.",
  "mock_taskcard_a",
  [
    { id: "a-folded", scenario: MAIN },
    { id: "a-open-notes", scenario: MAIN },
    { id: "a-open-clean", scenario: MAIN_NOFRESH },
  ],
);

const pipelineModel = variantModel(
  "preview:taskcardmockups-b",
  "turn-b",
  "Show the pipeline folded and open.",
  "mock_taskcard_b",
  [
    { id: "b-folded", scenario: MAIN },
    { id: "b-open-notes", scenario: MAIN },
  ],
);

const ledgerModel = variantModel(
  "preview:taskcardmockups-c",
  "turn-c",
  "Show the quiet ledger folded and open.",
  "mock_taskcard_c",
  [
    { id: "c-folded", scenario: MAIN },
    { id: "c-open-notes", scenario: MAIN },
  ],
);

const insetModel = variantModel(
  "preview:taskcardmockups-d",
  "turn-d",
  "Show the now inset folded and open.",
  "mock_taskcard_d",
  [
    { id: "d-folded", scenario: MAIN },
    { id: "d-open-notes", scenario: MAIN },
  ],
);

const stripModel = variantModel(
  "preview:taskcardmockups-e",
  "turn-e",
  "Show the segment strip folded and open.",
  "mock_taskcard_e",
  [
    { id: "e-folded", scenario: MAIN },
    { id: "e-open-notes", scenario: MAIN },
  ],
);

// The restyled tasks pane, live: the real TasksPanelBody fed by a fake
// client carrying the SAME five-task state the card sections render, so the
// two surfaces can be compared directly. Both theme panes mount the same
// body against the same tasksPanelStore entry (keyed by this dev
// sessionRef), so a single fetch serves both.
// One session ref for the cards and the pane demo alike: the Open-list
// buttons open the same session's pane the demo displays.
const PANE_SESSION_REF = SESSION_REF;
const PANE_CLIENT = new FakeClient("ready");
PANE_CLIENT.on("evener/tasks/list", () => ({ data: MAIN.state }));
connectionStore.getState().connect(PANE_CLIENT);
const paneModel: ThreadModel = {
  ...modelForSection("preview:taskcardmockups-pane", {
    id: "turn-pane",
    userText: "Compare the pane and the card on the same task state.",
    items: [],
  }),
  tasks: { total: 5, done: 2, remaining: 3 },
};

// Capture affordance for the headless one-shot screenshot path
// (google-chrome --headless=new --screenshot): with #pane-open in the URL
// the entry pre-opens the pane demo's settled group - the explicit store
// entry wins over Disclosure's collapsed fallback - and scrolls the section
// into view once mounted, so a single headless load can capture all five
// rows. Dev-only; nothing reads the hash otherwise.
if (location.hash === "#pane-open") {
  toggleDisclosure(`${PANE_SESSION_REF}\0settled-group`, false);
  requestAnimationFrame(() => {
    [...document.querySelectorAll("main > section")]
      .find((section) => section.querySelector("h2")?.textContent.includes("Tasks pane"))
      ?.scrollIntoView({ block: "start" });
  });
}

// Edge cases, all through the ladder renderer (the recommendation): the
// no-note rule, a two-task list, everything done, a cancellation as the
// most recent settle, and an append that lands beyond the window.
const edgesModel = modelForSection("preview:taskcardmockups-edges", {
  id: "turn-edges",
  userText: "Now the edge cases: a two-task list, everything done, a cancellation, and a late append.",
  items: [
    scenarioItem("edge-open-small", "mock_taskcard_a", SMALL, "turn-edges"),
    scenarioItem("edge-open-alldone", "mock_taskcard_a", ALLDONE, "turn-edges"),
    scenarioItem("edge-open-cancelled", "mock_taskcard_a", CANCELLED, "turn-edges"),
    scenarioItem("edge-open-append", "mock_taskcard_a", APPEND, "turn-edges"),
  ],
});

const root = document.getElementById("root");
if (!root) throw new Error("task card mock-ups require #root");
createRoot(root).render(
  <main className={GALLERY}>
    <h1>Inline task list card rework</h1>
    <p className={INTRO}>
      Five variants of the reworked card, rendered through the real transcript pipeline. The ruleset every variant
      obeys: the folded row names only the most recent update; the open body shows at most three tasks, ordered
      most-recently-settled, in-progress, next; a note appears only when this call added it. Click a summary line to
      fold and unfold; every section renders in dark and light.
    </p>
    <Section
      title="Production: the reworked card, in context"
      blurb="The production task_list descriptor as reworked (2026-09): settles folded with the latest-update line, opens to the window, notes only when fresh. The five mock variants below are the historical alternatives the rework chose among."
    >
      <TaskTranscript scope="preview:taskcardmockups-today" model={todayModel} />
    </Section>
    <Section
      title="Tasks pane · restyled"
      blurb="The real TasksPanelBody on the shared TaskCheck family: rows lead with the box glyphs (the empty box for open), the live row's latest note hangs bare in the prose face, and the notes timeline sets the same way. Fed by a fake client carrying the same five tasks the card sections render."
    >
      <TasksPanelBody sessionRef={PANE_SESSION_REF} model={paneModel} />
    </Section>
    <Section
      title="A · Ladder"
      blurb="The window as a vertical progression joined by a hairline spine through the glyphs. The just-added note sets in the prose face. Folded names the single latest touch (here, the auto-start)."
    >
      <TaskTranscript scope="preview:taskcardmockups-a" model={ladderModel} />
    </Section>
    <Section
      title="B · Pipeline"
      blurb="The whole window in one wrapping line, arrow-separated, each fragment clamped to one line. Folded reads state instead of event: Now: <working on>."
    >
      <TaskTranscript scope="preview:taskcardmockups-b" model={pipelineModel} />
    </Section>
    <Section
      title="C · Quiet ledger"
      blurb="The minimal-delta option: today's chrome exactly (aggregate head + meter, then rows), with the changelog swapped for the window. The diff against today is only the rows' content."
    >
      <TaskTranscript scope="preview:taskcardmockups-c" model={ledgerModel} />
    </Section>
    <Section
      title="D · Now inset"
      blurb="The in-progress task inside an evidence-inset box - the transcript's own look-here grammar - with the settled task above and the next one below."
    >
      <TaskTranscript scope="preview:taskcardmockups-d" model={insetModel} />
    </Section>
    <Section
      title="E · Segment strip"
      blurb="Progress first: one segment per task (settled fills quiet ink, working carries alive, open stays sunken), the aggregate sentence beneath, then the window rows without the ladder's spine."
    >
      <TaskTranscript scope="preview:taskcardmockups-e" model={stripModel} />
    </Section>
    <Section
      title="Edge cases · rendered through the Ladder"
      blurb="A two-task list (window shrinks to working + next), everything done (the window holds only the most recently completed), a cancellation as the most recent settle (struck, first slot), and a late append that lands beyond the window - only the folded line names it."
    >
      <TaskTranscript scope="preview:taskcardmockups-edges" model={edgesModel} />
    </Section>
  </main>,
);

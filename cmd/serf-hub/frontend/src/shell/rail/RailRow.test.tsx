import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import { Tree, type TreeRowInfo } from "../../widgets";
import railStyles from "./Rail.module.css";
import { activityGloss, cadenceStateFor, RailRow, type RailRowActions } from "./RailRow";
import type {
  InactiveFoldRailNode,
  LoadingRailNode,
  OverflowRailNode,
  ProjectRailNode,
  SessionRailNode,
} from "./railNodes";

afterEach(cleanup);

function apiNode(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "project:p1:local:a",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Fix flaky test",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

function apiProject(overrides: Partial<ApiTreeProject> = {}): ApiTreeProject {
  return { key: "p1", name: "Proj", sessions: [], ...overrides };
}

function sessionRailNode(session: ApiTreeNode, overrides: Partial<SessionRailNode> = {}): SessionRailNode {
  return { id: session.row_id, kind: "session", session, expanded: false, children: [], ...overrides };
}

function projectRailNode(project: ApiTreeProject, children: ProjectRailNode["children"] = []): ProjectRailNode {
  return { id: `projectnode:${project.key}`, kind: "project", project, expanded: false, children };
}

function loadingRailNode(): LoadingRailNode {
  return { id: "loading-1", kind: "loading" };
}

function overflowRailNode(count: number): OverflowRailNode {
  return { id: "projectnode:p1:overflow", kind: "overflow", count, pages: [] };
}

function inactiveFoldRailNode(count: number): InactiveFoldRailNode {
  return { id: "inactive:parent", kind: "inactiveFold", count, expanded: false, children: [] };
}

function info(overrides: Partial<TreeRowInfo> = {}): TreeRowInfo {
  return { depth: 0, expanded: false, hasChildren: false, toggle: vi.fn(), activate: vi.fn(), ...overrides };
}

function actions(overrides: Partial<RailRowActions> = {}): RailRowActions {
  return {
    onPinSectionRequest: vi.fn(),
    onMovePinSectionRequest: vi.fn(),
    onToggleArchiveSession: vi.fn(),
    onRenameRequest: vi.fn(),
    onDeleteSessionRequest: vi.fn(),
    onToggleFavoriteProject: vi.fn(),
    onToggleArchiveProject: vi.fn(),
    onDeleteProjectRequest: vi.fn(),
    ...overrides,
  };
}

async function openMenu(name: RegExp | string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name }));
  return user;
}

describe("cadenceStateFor", () => {
  test.each([
    ["errored", "failed"],
    ["awaiting", "needs-you"],
    ["active", "working"],
    ["warning", "needs-you"],
    ["idle", "idle"],
    ["ended", "ended"],
    ["notLoaded", "idle"],
    ["", "idle"],
    ["some-unknown-future-state", "idle"],
  ] as const)("maps wire state %s to Cadence state %s", (wireState, expected) => {
    expect(cadenceStateFor(wireState)).toBe(expected);
  });
});

// The signal dot (§ the dot earns its space): a row only shows a Cadence
// dot for a state that TELLS you something - working, waiting on you, failed. A
// quiet row shows no dot and, since the 2026-07-31 sidebar-density pass, holds
// no space for one either: the old always-reserved 6px gutter is gone, so a
// title's x-position now shifts one slot between quiet and signal rows -
// deliberate, matching how state already moves row height via the gloss line.
describe("signal gutter", () => {
  test.each([
    ["active", "Working"],
    ["awaiting", "Needs you"],
    ["warning", "Needs you"],
    ["errored", "Failed"],
  ] as const)("state %s shows a %s dot in the gutter", (state, label) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    const gutter = screen.getByTestId("rail-row-signal");
    expect(within(gutter).getByTestId("cadence-dot")).toBeTruthy();
    expect(within(gutter).getByRole("img", { name: label })).toBeTruthy();
  });

  test.each(["ended", "idle", "notLoaded", ""] as const)("state %s shows no dot and holds no slot", (state) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    // ...and the gutter itself is gone too - no space held for a dot that
    // is not there (the pre-density-pass behavior reserved it).
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row's rollup state follows the same rule", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "active" }))} info={info()} actions={actions()} />,
    );
    expect(within(screen.getByTestId("rail-row-signal")).getByTestId("cadence-dot")).toBeTruthy();

    rerender(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "ended" }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row with no rollup state at all shows no dot and holds no slot", () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });
});

describe("activityGloss", () => {
  test("states the humanized state alone when the session carries no branch", () => {
    expect(activityGloss(apiNode({ state: "active" }))).toBe("working");
  });

  test("joins state and branch, in that order", () => {
    expect(activityGloss(apiNode({ state: "awaiting", branch: "main" }))).toBe("your move · main");
  });

  // A plain "your move" (turn ended, nothing further queued) and a real
  // blocked ask_user question both wire up as state "awaiting" - the ONLY
  // wire signal telling them apart is ask_pending. Reusing hubapi.StateWord's
  // own vocabulary (Track A §2 ask-tiering: "Question waiting" vs "Your
  // move") here is what lets a person scanning the rail tell "the agent is
  // blocked on my answer" from "the agent finished, read it when you like"
  // without opening every amber row - see k9-navigation's persona panel,
  // where every persona hit this exact wall.
  test("an ask_pending awaiting session glosses as a question, not a generic move", () => {
    expect(activityGloss(apiNode({ state: "awaiting", ask_pending: true }))).toBe("question waiting");
  });

  test("omits an empty branch", () => {
    expect(activityGloss(apiNode({ state: "idle", branch: "" }))).toBe("idle");
  });

  // kata 59mx: hubapi.StateWord already gives "warning" its own word
  // ("Warning"), distinct from either "awaiting" band - this gloss used to
  // fold it into the same "waiting on you" text a plain awaiting session
  // gets, even though the two wire states are not the same situation.
  test("glosses warning as its own word, not a generic waiting-on-you", () => {
    expect(activityGloss(apiNode({ state: "warning" }))).toBe("warning");
  });

  // The model is a property of the session, not a reason to look at it - and
  // the session pane's own status strip reports it the moment you open the row.
  // On a triage surface it was noise on every row.
  test("never carries the model, whatever the state", () => {
    expect(activityGloss(apiNode({ state: "active", branch: "main", model: "opus" }))).toBe("working · main");
  });

  // Tier isn't dropped, it's relocated: it survives in the row's title tooltip,
  // where a fact a title cannot carry stays reachable without spending a line.
  test("never carries the tier on the visible line", () => {
    expect(activityGloss(apiNode({ state: "errored", tier: "archived" }))).toBe("failed");
  });
});

describe("loading row", () => {
  test("renders a non-interactive loading indicator", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByText(/loading/i)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  test("announces itself via role=status, like the top-level Skeleton", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByRole("status").textContent).toMatch(/loading/i);
  });
});

describe("overflow row", () => {
  test("says how many rows the server capped away", () => {
    render(<RailRow node={overflowRailNode(12)} info={info()} actions={actions()} />);
    expect(screen.getByText("+12 older")).toBeTruthy();
  });

  // Nothing to open and nothing to act on: the rows it counts were never sent
  // to the client, so a chevron or a menu would both be lies.
  test("offers nothing to click", () => {
    render(<RailRow node={overflowRailNode(3)} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("inactive-subagent fold row", () => {
  test("names what it hides and how many", () => {
    render(<RailRow node={inactiveFoldRailNode(3)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByText("Inactive subagents (3)")).toBeTruthy();
  });

  test("counts one in the singular", () => {
    render(<RailRow node={inactiveFoldRailNode(1)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByText("Inactive subagent (1)")).toBeTruthy();
  });

  // It stands for finished work, so it has no state to signal and nothing to
  // act on - the two things every session row spends its right edge on.
  test("carries no actions menu", () => {
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByRole("button", { name: /actions for/i })).toBeNull();
  });

  test("carries no cadence dot and no signal slot", () => {
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("toggles on click, the way its chevron does", async () => {
    const toggle = vi.fn();
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true, toggle })} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("Inactive subagents (2)"));
    expect(toggle).toHaveBeenCalled();
  });
});

describe("session row", () => {
  test("renders the session's title and a Cadence reflecting its state", () => {
    const session = apiNode({ title: "Fix flaky test", state: "active" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test")).toBeTruthy();
    // Cadence's wrapper carries the state as its accessible name (see
    // widgets/cadence) - "Working" is the family "active" maps to.
    expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  });

  test("clicking the label activates the row via info.activate", async () => {
    const rowInfo = info();
    render(<RailRow node={sessionRailNode(apiNode())} info={rowInfo} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("Fix flaky test"));
    expect(rowInfo.activate).toHaveBeenCalledTimes(1);
  });

  test("shows no chevron for a leaf session (info.hasChildren false)", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByTestId("rail-chevron")).toBeNull();
  });

  // The chevron gutter is reserved on EVERY row, filled only on a branch: a
  // leaf that rendered nothing here gained no chevron width, so its title
  // started further left than its branch siblings' and rows with children read
  // as a weird extra indent.
  test("a leaf session still reserves the chevron's gutter, empty", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    const gutter = screen.getByTestId("rail-row-chevron-gutter");
    expect(gutter).toBeTruthy();
    expect(gutter.querySelector("*")).toBeNull();
  });

  test("shows a chevron for a branch session (subagent cluster) that calls info.toggle", async () => {
    const rowInfo = info({ hasChildren: true, expanded: false });
    render(<RailRow node={sessionRailNode(apiNode())} info={rowInfo} actions={actions()} />);
    // The chevron is deliberately aria-hidden (decorative mouse shortcut -
    // see widgets/tree's own doc comment and RailRow.tsx's Chevron), so
    // it's found by test id rather than an accessible role query.
    await userEvent.setup().click(screen.getByTestId("rail-chevron"));
    expect(rowInfo.toggle).toHaveBeenCalledTimes(1);
  });

  // Same leading structure, same order, whether or not a row has children -
  // which is what makes one title x-position hold across a mixed tree. The
  // signal slot participates only when there is a dot (state "active" here);
  // a quiet row's text column follows the chevron gutter directly.
  test.each([true, false])(
    "the leading slots are chevron-gutter then signal-gutter (hasChildren %s)",
    (hasChildren) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state: "active" }))}
          info={info({ hasChildren })}
          actions={actions()}
        />,
      );
      const chevronGutter = screen.getByTestId("rail-row-chevron-gutter");
      const signal = screen.getByTestId("rail-row-signal");
      const row = chevronGutter.parentElement;
      expect(row).toBeTruthy();
      expect([...(row?.children ?? [])].slice(0, 2)).toEqual([chevronGutter, signal]);
    },
  );

  test("a quiet row holds no signal slot between the chevron gutter and the text", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "idle" }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    const chevronGutter = screen.getByTestId("rail-row-chevron-gutter");
    const row = chevronGutter.parentElement;
    expect(row).toBeTruthy();
    expect(row?.children[1]?.textContent).toContain("Fix flaky test");
  });

  test("shows a pin star when the session has a section assignment, hides it otherwise", () => {
    const { rerender } = render(
      <RailRow node={sessionRailNode(apiNode({ pin_section_id: "research" }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    rerender(
      <RailRow node={sessionRailNode(apiNode({ pin_section_id: undefined }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByTestId("favorite-star")).toBeNull();
  });

  // vbh8/§2.2: a derived amber count of needs-you descendants - distinct
  // from the row's own Cadence dot (which already goes amber when the
  // SESSION ITSELF needs you - see cadenceStateFor above). A leaf session
  // with no needs-you descendants shows no badge at all, even if it needs
  // you itself - the dot alone covers that case, so a redundant "0"/"1"
  // badge would double up on the same signal.
  test("shows a derived needs-you-descendant count Badge when a child needs you", () => {
    const session = apiNode({
      state: "active",
      children: [{ ...apiNode({ row_id: "child", ref: "local:child", state: "awaiting" }) }],
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("1")).toBeTruthy();
  });

  test("shows no Badge for a leaf session that itself needs you (the Cadence dot already covers it)", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "awaiting" }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("1")).toBeNull();
    expect(screen.queryByText("0")).toBeNull();
  });

  // vbh8 new capability, §2.3: row anatomy for the (already-existing)
  // subagent tree - a right-aligned relative timestamp OR the Task-7 Badge,
  // whichever slot applies, plus (on a signal row) the gloss line.
  test("shows a humanized activity line and a relative timestamp", () => {
    const session = apiNode({ state: "active", age: "2m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/working/i);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("2m");
  });

  // --- the gloss line is a SIGNAL, not row furniture ---------------------
  //
  // The rail is a triage surface: who needs me, and nothing else. A quiet row's
  // empty signal gutter and grey age already say "nothing happening here", so a
  // second line restating "idle" in words put the same fact at two altitudes on
  // the one surface that exists to be skimmed. Only a signal state earns the
  // line - and it's the SAME predicate that earns the dot, so the two never
  // disagree about whether a row matters.
  //
  // kata hxjn is the one deliberate exception: a row at depth 0 (a top-level
  // entry in the flat, cross-project Live/Pinned tiers - see
  // SessionRow's own showsProject comment) gets a second line for its
  // project even when otherwise quiet, because that fact has nowhere else to
  // live on a flat list. A depth>0 row (nested under its own ProjectRow, or
  // a subagent child) is unaffected - `info()`'s own default is depth 0, so
  // the tests below that want the OLD one-line-quiet-row behavior pass
  // depth: 1 explicitly.

  test.each([
    ["active", /working/i],
    ["awaiting", /your move/i],
    ["warning", /^warning/i],
    ["errored", /failed/i],
  ] as const)("a signal row (%s) keeps a gloss line leading with the state", (state, expected) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(expected);
  });

  // kata zq7g: the rail's only "waiting on you" signal used to be a 6px dot
  // plus uniformly grey (--ink-low) gloss text, identical for working/
  // needs-you/failed rows - a person had to already know to look at the dot
  // to tell them apart. The gloss text now carries the same family color as
  // its Cadence dot (cadenceStateFor), so a failed or needs-you row reads
  // distinctly even at a glance, no dot inspection required. Idle/ended never
  // render a gloss line at all (SIGNAL_STATES), so they need no family class.
  test.each([
    ["active", railStyles.activityAlive, "activityAlive"],
    ["awaiting", railStyles.activityAttention, "activityAttention"],
    ["warning", railStyles.activityAttention, "activityAttention"],
    ["errored", railStyles.activityDanger, "activityDanger"],
  ] as const)("a signal row (%s) tints its gloss text with the matching family color", (state, familyClass, name) => {
    if (familyClass === undefined) throw new Error(`Rail.module.css is missing the "${name}" class`);
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.className.split(" ")).toContain(familyClass);
  });

  // The one state where the SAME wire state ("awaiting") means two different
  // things depending on ask_pending - see the activityGloss describe block
  // above for why this distinction exists.
  test("an ask_pending awaiting row glosses as a blocked question, not a generic move", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "awaiting", ask_pending: true }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/question waiting/i);
  });

  test.each(["idle", "ended", "notLoaded", ""] as const)(
    "a quiet, nested row (%s, depth > 0) is title + age on one line, with no gloss at all",
    (state) => {
      render(
        <RailRow node={sessionRailNode(apiNode({ state, age: "3h" }))} info={info({ depth: 1 })} actions={actions()} />,
      );
      expect(screen.queryByTestId("rail-row-activity")).toBeNull();
      expect(screen.getByText("Fix flaky test")).toBeTruthy();
      expect(screen.getByTestId("rail-row-time").textContent).toBe("3h");
    },
  );

  // kata hxjn: the exception above, in the flat Live/Pinned tiers (depth 0).
  // A quiet row there still names its project, since a flat list gives it no
  // other way to say which project it belongs to.
  test.each(["idle", "ended", "notLoaded", ""] as const)(
    "a quiet, top-level row (%s, depth 0) names its project on a second line",
    (state) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state, age: "3h", project: "prime-radiant" }))}
          info={info({ depth: 0 })}
          actions={actions()}
        />,
      );
      expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant");
      expect(screen.getByTestId("rail-row-time").textContent).toBe("3h");
    },
  );

  // The dot and the gloss answer the same question in a NESTED row (depth >
  // 0, where hxjn's project line never applies) - one predicate
  // (SIGNAL_STATES) drives both there, which is what stops a nested row from
  // ever showing a dot with no explanation or an explanation with no dot. At
  // depth 0 that one-to-one correspondence is deliberately broken by the
  // project line (tested above and below).
  test.each(["active", "awaiting", "warning", "errored", "idle", "ended", "notLoaded", ""] as const)(
    "the dot and the gloss line agree for state %s on a nested row",
    (state) => {
      render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
      const hasDot = screen.queryByTestId("cadence-dot") !== null;
      const hasGloss = screen.queryByTestId("rail-row-activity") !== null;
      expect(hasGloss).toBe(hasDot);
    },
  );

  // At depth 0 a dot still always implies a gloss (a signal is never silent),
  // but the reverse no longer holds: a quiet top-level row shows a gloss line
  // for its project with no dot at all.
  test.each(["active", "awaiting", "warning", "errored", "idle", "ended", "notLoaded", ""] as const)(
    "a dot on a top-level row always implies a gloss line",
    (state) => {
      render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 0 })} actions={actions()} />);
      const hasDot = screen.queryByTestId("cadence-dot") !== null;
      const hasGloss = screen.queryByTestId("rail-row-activity") !== null;
      if (hasDot) expect(hasGloss).toBe(true);
    },
  );

  // Branch survives on a signal row because it distinguishes SIBLINGS in the
  // case that matters: two working sessions in one project on different
  // branches. Rendered nested (depth > 0) to isolate it from hxjn's project
  // line, tested separately below.
  test("a signal row's gloss carries the branch when the session has one", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", branch: "fix/thing" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("working · fix/thing");
  });

  // kata hxjn: a top-level (depth 0) signal row's gloss leads with the
  // project, then the usual state · branch join - project answers "where",
  // the rest answers "what's happening", in that reading order.
  test("a top-level signal row's gloss leads with the project, then state and branch", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", branch: "fix/thing", project: "prime-radiant" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant · working · fix/thing");
  });

  // Three facts of noise on every row: the model is a property of the session,
  // not a reason to look at it, and the session pane's own status strip reports
  // it the moment the row is opened.
  test("no row - signal or quiet - carries the model anywhere on its visible face", () => {
    const { rerender } = render(
      <RailRow node={sessionRailNode(apiNode({ state: "active", model: "opus" }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).not.toMatch(/opus/);

    rerender(
      <RailRow node={sessionRailNode(apiNode({ state: "idle", model: "opus" }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByText(/opus/)).toBeNull();
  });

  // --- a session that has never run says so ------------------------------
  //
  // An empty-prompt spawn starts a session dormant (kata ytpa), and the server
  // reports it "idle" - the same word a session that ran and went quiet gets.
  // Every quiet row is title + age, so the two were the same row. Worse, the
  // age READ as activity: it falls back to the creation time, so a session that
  // has never done anything showed a confident "now" or "4m".
  //
  // The row spends no new space on this. The one slot that was actively lying -
  // the age - is the slot that carries the correction, so a dormant row stays
  // one line and the mobile drawer still shows the same six rows.

  test("a dormant row says it has not started, in place of an age that would read as activity", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: true }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-not-started").textContent).toBe("Not started");
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  test("a session that has run keeps its age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: false }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-time").textContent).toBe("4m");
    expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
  });

  // "Not started" is a fact about a row's history, not a call for help. The
  // signal gutter is reserved for the states worth crossing the room for
  // (working / needs you / failed) - a dot here would put a dormant session in
  // that company, and a rail full of dots is a rail whose dots mean nothing.
  // Rendered nested (depth > 0) so hxjn's project line - orthogonal to
  // dormancy - doesn't participate in this assertion; see the dedicated
  // depth-0 case just below.
  test("a dormant, nested row earns no dot and no gloss line", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", dormant: true }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-activity")).toBeNull();
  });

  // kata hxjn: a dormant row still needs its project named when it's a
  // top-level Live/Pinned row - dormancy says nothing about which project a
  // flat row belongs to.
  test("a dormant, top-level row still names its project, with no dot", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", dormant: true, project: "prime-radiant" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant");
  });

  // The moment a dormant session is given something to do it is working, and
  // the row must say THAT. Dormancy is only ever the most useful thing to
  // report on a row that is otherwise quiet.
  test.each(["active", "awaiting", "warning", "errored"] as const)(
    "a signal state (%s) outranks dormancy in the right slot",
    (state) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state, age: "now", dormant: true }))}
          info={info()}
          actions={actions()}
        />,
      );
      expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
      expect(screen.getByTestId("rail-row-time").textContent).toBe("now");
    },
  );

  // The visible row gave up its age, so the tooltip has to keep it - the same
  // contract every other fact this row drops is held to.
  test("a dormant row's tooltip keeps the age its visible line gave up", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: true }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · not started · 4m");
  });

  // --- what the visible row drops stays reachable on hover --------------
  //
  // Tier is real information a title cannot carry, and a quiet row no longer
  // prints its state - so both land in the row's own title tooltip, which
  // already existed for truncated titles.

  test("a quiet row's title tooltip carries the state word its visible line no longer prints", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "ended" }))} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · ended");
  });

  test("the tier rides the title tooltip on both quiet and signal rows", () => {
    const { rerender } = render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", tier: "archived" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · idle · archived");

    // A signal row already prints its state, so the tooltip doesn't repeat it.
    rerender(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", tier: "archived" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · archived");
  });

  // "current" is the unremarkable default state of a session - the same
  // exclusion the old visible line made, kept in its new home.
  test("the unremarkable 'current' tier is omitted from the tooltip too", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", tier: "current" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test");
  });

  test("a needs-you count takes the right slot instead of the timestamp", () => {
    const session = apiNode({
      state: "active",
      age: "2m",
      children: [apiNode({ row_id: "child", ref: "local:child", state: "awaiting" })],
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
    expect(screen.getByText("1")).toBeTruthy(); // the Badge from Task 7
  });

  test("shows no timestamp when the session carries no age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", age: undefined }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  test("menu offers 'Pin this session…' for an unassigned top-level session", async () => {
    const acts = actions();
    const session = apiNode({ pin_section_id: undefined, ref: "local:a" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
    expect(acts.onPinSectionRequest).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Move pinned session…' for an assigned top-level session", async () => {
    const acts = actions();
    const session = apiNode({ pin_section_id: "research" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Move pinned session…" }));
    expect(acts.onMovePinSectionRequest).toHaveBeenCalledWith(session);
  });

  test("menu omits Rename when the session does not support it", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ rename: false }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("menu offers Rename when the session supports it, and calls onRenameRequest on select", async () => {
    const acts = actions();
    const session = apiNode({ rename: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(acts.onRenameRequest).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Archive' for a session outside the archived tier, and calls onToggleArchiveSession", async () => {
    const acts = actions();
    const session = apiNode({ tier: "current" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive" }));
    expect(acts.onToggleArchiveSession).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Unarchive' for a session already in the archived tier", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ tier: "archived" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive" })).toBeTruthy();
  });

  // Archive is a decision about a TOP-LEVEL row, and only a top-level row can
  // act on it: the server stores one archive decision per session id, and a
  // nested row has no independent existence in the tree its parent isn't
  // already deciding for. hubcore's nodeKind (internal/hubcore/tree.go) names
  // the three kinds that are never top-level - "subagent" (nested under its
  // parent), "fork" (a snapshotted original nested under the branch that
  // superseded it), and the synthetic "cluster" fold row - so `kind` is the
  // whole test, at any depth.
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits Archive on a ${kind} row - only top-level sessions are archivable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, tier: "current" }))} info={info()} actions={actions()} />);
      // Such a row may now have no menu trigger at all (nothing left to put in
      // it); either way, what matters is that neither verb is reachable.
      const trigger = screen.queryByRole("button", { name: /actions for/i });
      if (trigger) await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Archive" })).toBeNull();
      expect(screen.queryByRole("menuitem", { name: "Unarchive" })).toBeNull();
    });
  }

  // Delete (kata n15j) is a decision about a TOP-LEVEL LOCAL session: it
  // targets a stable local session ref (identifier.ValidateSessionID via
  // cmd/serf-hub/web_api_session_delete.go), so it is offered unconditionally
  // for a top-level local row - including a live one, which the server
  // refuses via the same skipped/toast path deleteProject already uses for a
  // session that raced back to live (no client-side liveness gate to
  // duplicate the server's own crash-vs-live predicate).
  test("menu offers 'Delete…' for a top-level local session and calls onDeleteSessionRequest on select", async () => {
    const acts = actions();
    const session = apiNode({ host_id: "local" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
    expect(acts.onDeleteSessionRequest).toHaveBeenCalledWith(session);
  });

  // "do not offer this capability for remote-source threads" (kata n15j) -
  // the menu itself withholds Delete for a non-local session rather than
  // relying solely on the server's own isLocalRouteID refusal.
  test("menu omits Delete for a remote-source session", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ host_id: "codex" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
  });

  // Delete is scoped like Archive: only a top-level row names a real,
  // independently deletable session (see the Archive loop's own comment
  // above for why these three kinds are never top-level).
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits Delete on a ${kind} row - only top-level sessions are deletable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, host_id: "local" }))} info={info()} actions={actions()} />);
      const trigger = screen.queryByRole("button", { name: /actions for/i });
      if (trigger) await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
    });
  }

  // Favorite is scoped for the same reason as Archive, and the server proves
  // it: POST /api/favorite accepts a subagent id and writes the decision, but
  // the Pinned tier is drawn only from a project's top-level Current+Recent
  // sessions (web_api_tree.go), so the row never appears there and never comes
  // back favorite:true - a menu item that silently does nothing. On a cluster
  // it is worse: the id is a synthetic SHA-derived "cluster:<hex>" naming no
  // session at all, so it writes a decision row nothing will ever clean up.
  // Both verified against a live hub.
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits pin and move on a ${kind} row`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind }))} info={info()} actions={actions()} />);
      const trigger = screen.queryByRole("button", { name: /actions for/i });
      if (trigger) {
        await openMenu(/actions for/i);
        expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
        expect(screen.queryByRole("menuitem", { name: "Move pinned session…" })).toBeNull();
      }
    });
  }

  // With favorite, rename (already withheld server-side - `rename` is absent on
  // every nested/synthetic node) and archive all gone, there is nothing left to
  // put in a subagent's menu, and ActionsMenu renders no trigger for an empty
  // item list. The row stays clickable to open the session; it just carries no
  // "..." button.
  test("a subagent row offers no actions menu at all", () => {
    render(<RailRow node={sessionRailNode(apiNode({ kind: "subagent" }))} info={info()} actions={actions()} />);
    expect(screen.queryByRole("button", { name: /actions for/i })).toBeNull();
  });

  test("a top-level local session still offers all four actions", async () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "session", rename: true, host_id: "local" }))}
        info={info()}
        actions={actions()}
      />,
    );
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Rename" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Archive" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Delete…" })).toBeTruthy();
  });

  // Title-first row (rail truncation round): the branch is secondary metadata
  // that used to sit in the row's main line as a flex:none sibling, so at the
  // rail's 280px it took its width off the top of the ONE thing that identifies
  // a row. It rides the gloss line, which ellipsizes on its own; the title keeps
  // the whole main line minus the (short, fixed) age.
  test("keeps the branch out of the title's line, on the gloss line instead", () => {
    const session = apiNode({ state: "active", branch: "main", age: "47m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/main/);
    // The row's main line holds the title and the age, and nothing else
    // that reserves width: every other text node lives on line two.
    const title = screen.getByText("Fix flaky test");
    const mainLine = title.parentElement?.parentElement;
    expect(mainLine).toBeTruthy();
    const mainLineText = [...(mainLine?.children ?? [])]
      .filter((child) => child !== title.parentElement)
      .map((child) => child.textContent)
      .join(" ");
    expect(mainLineText).not.toMatch(/main/);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("47m");
  });

  // vitest leaves CSS Modules unprocessed (vite.config.ts sets no test.css),
  // so the rule that actually keeps a long gloss from wrapping the row to a
  // third line is only checkable against the stylesheet text - the same way
  // StackHost.test.tsx / radiogroup.test.tsx pin their own layout-critical
  // declarations.
  test("the activity line's stylesheet rule ellipsizes rather than wraps, so metadata never grows the row", () => {
    const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "Rail.module.css"), "utf8");
    const activityRule = /\.activity\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(activityRule).toMatch(/white-space:\s*nowrap/);
    expect(activityRule).toMatch(/text-overflow:\s*ellipsis/);
    expect(activityRule).toMatch(/overflow:\s*hidden/);
  });

  // The tooltip's original job, unchanged: a truncated title stays recoverable.
  // The title always LEADS the tooltip, so the recovery still works even now
  // that the tooltip carries the row's dropped facts after it.
  test("a truncated title stays readable via a hover tooltip", () => {
    const long = "It looks like a lot of the sidebar rows are truncating their titles";
    render(
      <RailRow node={sessionRailNode(apiNode({ title: long, state: "active" }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByText(long).getAttribute("title")).toBe(long);
  });

  test("the activity line carries its own full text as a tooltip, since it ellipsizes too", () => {
    const session = apiNode({ state: "active", model: "opus", branch: "feature/long-branch-name" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.getAttribute("title")).toBe(activity.textContent);
  });

  // A live-tier row's own Tier/PinSectionID/Rename fields must all survive the
  // duplicate projection. RailRow reads pin_section_id/rename directly,
  // regardless of the session's real decisions, since handleAPITree's Live
  // loop bypassed the tier-stamping helper entirely. RailRow never gated
  // these on tier itself - it just reads session.favorite/session.rename
  // directly, same as every other row - so once the hub fix landed, this
  // was already correct with no rail-side code change; pinned explicitly
  // here (rather than left to incidental coverage from fixtures that never
  // set tier at all) since a live row is the realistic shape a reviewer
  // would specifically want proof for.
  test("pin star, Move, and Rename all work on a live-tier duplicate", async () => {
    const acts = actions();
    const session = apiNode({ tier: "live", pin_section_id: "research", rename: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);

    expect(screen.getByTestId("favorite-star")).toBeTruthy();
    const user = await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Move pinned session…" })).toBeTruthy();
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(acts.onRenameRequest).toHaveBeenCalledWith(session);
  });
});

describe("project row", () => {
  test("renders the project's name and a Cadence reflecting its rollup state", () => {
    const project = apiProject({ name: "prime-radiant", rollup_state: "errored" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
  });

  test("shows an attention Badge when rollup_attn is nonzero, hides it when zero", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_attn: 3 }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByText("3")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ rollup_attn: 0 }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("0")).toBeNull();
  });

  test("menu offers 'Archive project' for an active project and calls onToggleArchiveProject", async () => {
    const acts = actions();
    const project = apiProject({ is_archived: false });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive project" }));
    expect(acts.onToggleArchiveProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Unarchive project' for an already-archived project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ is_archived: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive project" })).toBeTruthy();
  });

  test("menu offers 'Delete project…' and calls onDeleteProjectRequest on select", async () => {
    const acts = actions();
    const project = apiProject();
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));
    expect(acts.onDeleteProjectRequest).toHaveBeenCalledWith(project);
  });

  test("a childless project reserves the chevron gutter too, so its name lines up with a parent project's", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: false })} actions={actions()} />,
    );
    expect(screen.getByTestId("rail-row-chevron-gutter").querySelector("*")).toBeNull();

    rerender(<RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: true })} actions={actions()} />);
    expect(within(screen.getByTestId("rail-row-chevron-gutter")).getByTestId("rail-chevron")).toBeTruthy();
  });

  test("menu never offers Rename for a project row - only sessions can be renamed", async () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("shows a favorite star when the project is favorited, hides it otherwise", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ favorite: false }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("favorite-star")).toBeNull();
  });

  test("menu offers 'Add to pinned' for an unfavorited project and calls onToggleFavoriteProject on select", async () => {
    const acts = actions();
    const project = apiProject({ favorite: false, key: "p1" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(acts.onToggleFavoriteProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Remove from pinned' for a favorited project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Remove from pinned" })).toBeTruthy();
  });

  test("the synthetic '(no project)' bucket gets no actions menu at all - archive/delete always 400 for it server-side", () => {
    render(
      <RailRow
        node={projectRailNode(apiProject({ key: "no-project", name: "(no project)" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.queryByRole("button")).toBeNull();
  });
});

// --- fix-wave: nested Menu triggers must not corrupt Tree's roving
// tabindex (Important) -----------------------------------------------
//
// These render RailRow through a REAL Tree (not the hand-built `info`
// double every other test in this file uses) - the bug this covers is
// specifically about Tree's own keyboard/focus machinery interacting with
// RailRow's content, which a fake `info` object can't exercise.
describe("roving-tabindex integration (Tree + RailRow)", () => {
  function twoSessionRows(): [SessionRailNode, SessionRailNode] {
    return [
      sessionRailNode(apiNode({ row_id: "rowA", ref: "local:a", title: "Row A" })),
      sessionRailNode(apiNode({ row_id: "rowB", ref: "local:b", title: "Row B" })),
    ];
  }

  function renderTree(nodes: SessionRailNode[]) {
    return render(
      <Tree
        nodes={nodes}
        onToggle={() => {}}
        onActivate={() => {}}
        renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
      />,
    );
  }

  test("only the roving treeitem is a Tab stop - neither row's actions trigger is", () => {
    renderTree(twoSessionRows());
    const treeitems = screen.getAllByRole("treeitem");
    expect(treeitems.map((el) => el.tabIndex)).toEqual([0, -1]); // Row A (first) starts as the roving one

    const triggers = screen.getAllByRole("button", { name: /actions for/i });
    expect(triggers).toHaveLength(2);
    for (const trigger of triggers) expect(trigger.tabIndex).toBe(-1);
  });

  test("Tab from before the tree lands on the roving treeitem, never a row's own trigger", async () => {
    render(
      <>
        <button type="button">Before</button>
        <Tree
          nodes={twoSessionRows()}
          onToggle={() => {}}
          onActivate={() => {}}
          renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
        />
      </>,
    );
    const user = userEvent.setup();
    act(() => screen.getByRole("button", { name: "Before" }).focus());
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("treeitem", { name: /Row A/ }));
  });

  test("ArrowDown on a row's trigger opens the menu; the tree's roving tabindex survives closing it again (post-Escape corruption probe)", () => {
    // Reproduces the reviewer's exact probe, inverted. The corruption is
    // NOT visible right after opening - FocusScope's own mount effect
    // (widgets/focusscope/index.tsx) captures document.activeElement as
    // its restore target, THEN focuses the popup's first item; that
    // second focus move bubbles back up to Row A's own treeitem (the
    // popup is rendered INSIDE it) and reasserts currentId="rowA" as a
    // side effect, momentarily masking the bug. But WITHOUT
    // stopPropagation, Tree's own moveTo("rowB") already ran (and moved
    // real DOM focus to Row B's treeitem) BEFORE that effect captured its
    // restore target - so the restore target FocusScope captured is Row
    // B's treeitem, not Row A's trigger. Closing the menu (Escape unmounts
    // FocusScope, running its cleanup) restores focus to that stale
    // target: Row B, silently stealing the roving tabindex out from under
    // Row A even though the menu that just closed belonged to Row A.
    renderTree(twoSessionRows());
    const rowATreeitem = screen.getByRole("treeitem", { name: /Row A/ });
    const rowBTreeitem = screen.getByRole("treeitem", { name: /Row B/ });
    const rowATrigger = within(rowATreeitem).getByRole("button", { name: /actions for/i });

    act(() => rowATrigger.focus());
    fireEvent.keyDown(rowATrigger, { key: "ArrowDown" });
    expect(screen.getByRole("menu")).toBeTruthy(); // the menu still opens

    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull(); // closed

    expect(rowATreeitem.tabIndex).toBe(0); // still Row A's roving tabindex...
    expect(rowBTreeitem.tabIndex).toBe(-1); // ...not silently moved to Row B
    expect(document.activeElement).toBe(rowATrigger); // and focus is back on Row A's own trigger
  });
});

// --- the actions overlay the row's right edge -------------------------
//
// jsdom applies no stylesheet at all (vite.config.ts's test block enables
// no `css` processing), so "hovering a row costs zero layout width" is not
// assertable against a rendered tree here. These read Rail.module.css off
// disk and pin the structure that makes it true - same mechanism as
// styles/display-gates.test.ts and widgets/tooltip's own touch gate.
describe("row actions overlay (Rail.module.css)", () => {
  const RAIL_CSS = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "Rail.module.css"), "utf8");
  // Block comments stripped so a class or token named only in prose can
  // never satisfy an assertion (same discipline as token-contract.test.ts).
  const CSS = RAIL_CSS.replace(/\/\*[\s\S]*?\*\//g, " ");

  function ruleFor(selector: string): string | null {
    const escaped = selector.replace(/[.[\]"^$*+?()|{}\\]/g, "\\$&");
    const match = new RegExp(`(?:^|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(CSS);
    return match ? match[1]! : null;
  }

  test("actions are taken out of flow and pinned to the row's right edge, so revealing them shifts nothing", () => {
    const actionsRule = ruleFor(".actions");
    expect(actionsRule).not.toBeNull();
    expect(actionsRule).toMatch(/position:\s*absolute/);
    expect(actionsRule).toMatch(/right:\s*0/);
    // Absolute positioning only resolves against .row if .row is itself a
    // containing block; without this the overlay escapes to the nearest
    // positioned ancestor (the scrolling rail body, or the viewport).
    expect(ruleFor(".row")).toMatch(/position:\s*relative/);
  });

  test("the reveal drives the actions container, not just its buttons - the mask has to hide too", () => {
    // An opacity that lives on `.actions button` alone would leave the
    // container's own masking background painted at rest, permanently
    // covering the timestamp it sits on top of.
    expect(ruleFor(".actions")).toMatch(/opacity:\s*0/);
    const reveal = /([^{}]*)\{\s*opacity:\s*1;?\s*\}/g;
    const revealRules = [...CSS.matchAll(reveal)].map((m) => m[1]!.trim());
    const rule = revealRules.find((s) => s.includes(".row:hover"));
    expect(rule, "row hover must reveal the actions").toBeTruthy();
    // Split on commas and require each of the three reveal paths to TARGET
    // `.actions` itself. A trailing descendant (`.row:hover .actions button`)
    // fails these: the container - and so its mask - would stay at opacity 0.
    const targets = rule!.split(",").map((s) => s.trim());
    expect(targets).toEqual(
      expect.arrayContaining([
        ".row:hover .actions",
        '[role="treeitem"]:focus .actions',
        '.actions:has(button[aria-expanded="true"])',
      ]),
    );
  });

  test("the overlay masks what it covers with the rail's own surface token", () => {
    const actionsRule = ruleFor(".actions");
    // Same token .rail declares as its background - a row has no hover
    // surface of its own (widgets/tree/tree.module.css paints none), so
    // this is literally what shows through behind a row.
    expect(ruleFor(".rail")).toMatch(/background:\s*var\(--surface-1\)/);
    expect(actionsRule).toMatch(/background:[^;]*var\(--surface-1\)/);
  });

  test("the mask's leading edge fades in, so it never slices covered text mid-glyph", () => {
    // A flat opaque background cuts whatever it covers at a hard vertical
    // line - a half-rendered letter at the overlay's left edge reads as a
    // rendering bug. A gradient ramps the mask in from transparent instead.
    const actionsRule = ruleFor(".actions");
    expect(actionsRule).toMatch(/linear-gradient\(\s*to right\s*,\s*transparent\s*,/);
    // The buttons must sit past the ramp (padding) rather than on top of
    // the partly-faded text the ramp exists to soften.
    expect(actionsRule).toMatch(/padding-left:\s*var\(--space-\d\)/);
  });

  // Both leading gutters reserve a FIXED width and refuse to flex, which is
  // what makes a title's x-position a constant across a mixed tree. jsdom
  // applies no stylesheet, so this is only checkable against the (comment-
  // stripped) stylesheet text.
  test.each([".chevron", ".signal"])("the %s gutter reserves a fixed width and never flexes", (selector) => {
    const rule = ruleFor(selector);
    expect(rule).not.toBeNull();
    expect(rule).toMatch(/width:\s*(var\(--space-\d+\)|\d+px)/);
    expect(rule).toMatch(/flex:\s*none|flex-shrink:\s*0/);
  });

  test("touch keeps the actions in flow beside the timestamp instead of permanently masking it", () => {
    // Below the mobile breakpoint the actions are always visible (no hover
    // to reveal them), so an overlay there would hide the age forever.
    const media = /@media\s*\(max-width:\s*899px\)\s*\{([\s\S]*?)\n\}/g;
    const blocks = [...CSS.matchAll(media)].map((m) => m[1]!);
    const actionsBlock = blocks.find((b) => b.includes(".actions"));
    expect(actionsBlock, "the 899px block must still address .actions").toBeTruthy();
    expect(actionsBlock).toMatch(/position:\s*static/);
    expect(actionsBlock).toMatch(/opacity:\s*1/);
    expect(actionsBlock).toMatch(/background:\s*none/);
  });
});

// A row that cannot be pinned must not display as pinned. The wire can still
// carry favorite:true on a nested or synthetic node - from a decision written
// before pinning was scoped, or by a direct API call - and rendering the star
// there is a dead end: the menu offers no way to take it off. Suppressing it
// keeps "only top-level sessions can be pinned" true in both directions.
describe("pin star follows the same scoping as the pin action", () => {
  test("a top-level session shows its star", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "session", pin_section_id: "research" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();
  });

  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`a ${kind} row shows no star even when the wire carries a section assignment`, () => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ kind, pin_section_id: "research" }))}
          info={info()}
          actions={actions()}
        />,
      );
      expect(screen.queryByTestId("favorite-star")).toBeNull();
    });
  }
});

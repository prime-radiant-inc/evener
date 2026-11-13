import type { Source } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { holdLiveBands, liveBandFoldOf, liveBandId, liveBandNodes, liveBandRefs, railLiveBands } from "./liveBands";
import type { RailSession } from "./railNodes";

function row(ref: string, overrides: Partial<RailSession> = {}): RailSession {
  const [host = "local", id = ref] = ref.split(":");
  return {
    ref,
    row_id: `navigation:${ref}`,
    host_id: host,
    session_id: id,
    title: ref,
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}
const at = (minute: number) => new Date(Date.UTC(2026, 1, 1, 12, minute)).toISOString();
const refsOf = (rows: readonly { session: { ref: string } }[]) => rows.map((item) => item.session.ref);
const open = (_id: string, defaultExpanded: boolean) => defaultExpanded;
const sources: Source[] = [
  { id: "local", label: "this host", kind: "local", online: true },
  { id: "devbox", label: "devbox", kind: "appwire", online: true },
  { id: "buildbox", label: "buildbox", kind: "appwire", online: false },
];

describe("railLiveBands", () => {
  test("orders Needs you by urgency then age, keeps the hub's Working order, and rests Idle newest first", () => {
    const live = [
      row("local:warn", { state: "warning", updated_at: at(1) }),
      row("local:ask-new", { state: "awaiting", ask_pending: true, updated_at: at(9) }),
      row("local:ask-old", { state: "awaiting", ask_pending: true, updated_at: at(2) }),
      row("local:failed", { state: "errored", updated_at: at(8) }),
      row("local:work-b", { state: "active" }),
      row("local:work-a", { state: "active" }),
      row("local:idle-old", { turn_ended_at: at(3) }),
      row("local:idle-new", { turn_ended_at: at(7) }),
    ];
    const bands = railLiveBands(live, []);
    expect(refsOf(bands.needsYou)).toEqual(["local:failed", "local:ask-old", "local:ask-new", "local:warn"]);
    expect(refsOf(bands.working)).toEqual(["local:work-b", "local:work-a"]);
    expect(refsOf(bands.idle)).toEqual(["local:idle-new", "local:idle-old"]);
    expect(liveBandRefs(bands)).toEqual([...refsOf(bands.needsYou), ...refsOf(bands.working), ...refsOf(bands.idle)]);
  });

  test("a needs-you row beyond Live's loaded pages still shows, once", () => {
    const shared = row("local:both", { state: "awaiting", ask_pending: true });
    const bands = railLiveBands([shared, row("local:idle")], [shared, row("local:far", { state: "errored" })]);
    expect(refsOf(bands.needsYou)).toEqual(["local:far", "local:both"]);
    expect(liveBandRefs(bands)).toHaveLength(3);
  });

  test("a resting session with a job still running sits in Working, after the sessions mid-turn", () => {
    const bands = railLiveBands(
      [row("local:job", { state: "idle", running_job_count: 1 }), row("local:turn", { state: "active" })],
      [],
    );
    expect(refsOf(bands.working)).toEqual(["local:turn", "local:job"]);
    expect(bands.working[1]?.state).toBe("working");
    expect(bands.idle).toEqual([]);
  });

  test("a warning that blocks nothing yields to a job still running, and a blocking one does not", () => {
    const bands = railLiveBands(
      [
        row("local:warn-job", { state: "warning", running_job_count: 1 }),
        row("local:warn-ask-job", { state: "warning", ask_pending: true, running_job_count: 1 }),
        row("local:failed-job", { state: "errored", running_job_count: 1 }),
        row("local:warn", { state: "warning" }),
      ],
      [],
    );
    expect(refsOf(bands.working)).toEqual(["local:warn-job"]);
    expect(refsOf(bands.needsYou)).toEqual(["local:failed-job", "local:warn-ask-job", "local:warn"]);
  });

  test("an approval only the needs-you section reports stays in Needs you over a Live copy that reads as working", () => {
    const live = row("local:esc", { state: "active" });
    const queued = { ...live, row_id: "navigation:needs_you:local:esc" };
    const bands = railLiveBands([live], [queued]);
    expect(refsOf(bands.needsYou)).toEqual(["local:esc"]);
    expect(bands.needsYou[0]?.state).toBe("approval");
    expect(bands.working).toEqual([]);
  });

  test("a needs-you session the rail files as archived still shows: the hub's tier never decays", () => {
    const stale = row("local:stale", { state: "awaiting", ask_pending: true, archived: true });
    const bands = railLiveBands([row("local:idle")], [stale]);
    expect(refsOf(bands.needsYou)).toEqual(["local:stale"]);
  });

  test("while held, a row keeps the band and place it had, with its fresh facts; new rows join their band", () => {
    const before = railLiveBands([row("local:a", { state: "active" }), row("local:b"), row("local:gone")], []);
    const after = railLiveBands(
      [row("local:a", { state: "awaiting", ask_pending: true }), row("local:b"), row("local:new", { state: "active" })],
      [],
    );
    const held = holdLiveBands(before, after);
    expect(refsOf(held.needsYou)).toEqual([]);
    expect(refsOf(held.working)).toEqual(["local:a", "local:new"]);
    expect(held.working[0]?.state).toBe("question");
    expect(refsOf(held.idle)).toEqual(["local:b"]);
  });

  test("a live row the shared model bands nowhere rests at the end of Idle instead of leaving Live", () => {
    const live = [
      row("buildbox:gone", { host_id: "buildbox", state: "active", offline: true }),
      row("local:unloaded", { state: "notLoaded" }),
      row("local:idle"),
    ];
    const bands = railLiveBands(live, []);
    expect(refsOf(bands.idle)).toEqual(["local:idle", "buildbox:gone", "local:unloaded"]);
    expect(liveBandRefs(bands).sort()).toEqual(live.map((item) => item.ref).sort());
  });

  test("names the fold that holds a ref, and none for a ref Live does not show", () => {
    const bands = railLiveBands([row("local:work", { state: "active" }), row("local:idle")], []);
    expect(liveBandFoldOf(bands, "local:work")).toBe(liveBandId("working"));
    expect(liveBandFoldOf(bands, "local:idle")).toBe(liveBandId("idle"));
    expect(liveBandFoldOf(bands, "local:missing")).toBeUndefined();
  });
});

describe("liveBandNodes", () => {
  test("renders a branch only for a band with rows, in band order, carrying each row's class", () => {
    const bands = railLiveBands([row("local:idle"), row("local:ask", { state: "awaiting", ask_pending: true })], []);
    const nodes = liveBandNodes(bands, sources, open);
    expect(nodes.map((node) => node.band)).toEqual(["needsYou", "idle"]);
    expect(nodes.map((node) => node.id)).toEqual([liveBandId("needsYou"), liveBandId("idle")]);
    expect(nodes[0]?.children.map((child) => child.live)).toEqual(["question"]);
    expect(nodes.every((node) => node.expanded === true)).toBe(true);
  });

  test("a fold the reader closed stays closed", () => {
    const bands = railLiveBands([row("local:idle")], []);
    const [idle] = liveBandNodes(bands, sources, (id, fallback) => (id === liveBandId("idle") ? false : fallback));
    expect(idle?.expanded).toBe(false);
    expect(idle?.children).toHaveLength(1);
  });

  test("names no host while Live sits on one host", () => {
    const bands = railLiveBands([row("devbox:a"), row("devbox:b")], []);
    const hosts = liveBandNodes(bands, sources, open).flatMap((node) => node.children.map((child) => child.liveHost));
    expect(hosts).toEqual([undefined, undefined]);
  });

  test("once Live spans hosts, only the rows away from this hub name theirs", () => {
    const bands = railLiveBands([row("local:a"), row("devbox:b"), row("devbox:c")], []);
    const named = new Map(
      liveBandNodes(bands, sources, open).flatMap((node) =>
        node.children.map((child) => [child.session.ref, child.liveHost?.label] as const),
      ),
    );
    expect(named.get("local:a")).toBeUndefined();
    expect(named.get("devbox:b")).toBe("devbox");
    expect(named.get("devbox:c")).toBe("devbox");
  });

  test("a row on an offline host says so even when it is the only host in Live", () => {
    const bands = railLiveBands([row("buildbox:a")], []);
    const [node] = liveBandNodes(bands, sources, open).flatMap((band) => band.children);
    expect(node?.liveHost).toEqual({ label: "buildbox", online: false });
  });

  test("a row keeps one identity whichever section supplied it, so it never remounts when Live's copy arrives", () => {
    const fromQueue = row("local:ask", {
      state: "awaiting",
      ask_pending: true,
      row_id: "navigation:needs_you:local:ask",
    });
    const fromLive = { ...fromQueue, row_id: "navigation:live:local:ask" };
    const idOf = (bands: ReturnType<typeof railLiveBands<RailSession>>) =>
      liveBandNodes(bands, sources, open)[0]?.children[0]?.id;
    expect(idOf(railLiveBands([], [fromQueue]))).toBe(idOf(railLiveBands([fromLive], [fromQueue])));
  });

  test("unchanged rows keep their nodes, so a rebuild re-renders nothing", () => {
    const live = [row("local:idle"), row("local:work", { state: "active" })];
    const first = liveBandNodes(railLiveBands(live, []), sources, open);
    const second = liveBandNodes(railLiveBands(live, []), sources, open);
    expect(second.map((node, index) => node === first[index])).toEqual([true, true]);
  });

  test("a row that changes class gets a fresh node in its new band", () => {
    const working = row("local:a", { state: "active" });
    const before = liveBandNodes(railLiveBands([working], []), sources, open)[0]?.children[0];
    const asking = { ...working, state: "awaiting", ask_pending: true };
    const after = liveBandNodes(railLiveBands([asking], []), sources, open);
    expect(after.map((node) => node.band)).toEqual(["needsYou"]);
    expect(after[0]?.children[0]).not.toBe(before);
  });
});

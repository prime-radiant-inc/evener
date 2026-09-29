import type { NavigationSessionSummary } from "@evener/appwire-client";
import { expect, it } from "vitest";
import type { BoardState } from "./attention";
import { selectionActions, toggleSelected } from "./selection";

/** A ref localSessionId accepts: "local:" and 22 base62 characters. */
const local = (letter: string) => `local:${letter.repeat(22)}`;
const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref.replace(/^local:/, ""),
	title: ref,
	project: "evener",
	state: "awaiting",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const chosen = (summary: NavigationSessionSummary, state: BoardState, archived = false) => ({
	item: { row: summary, state },
	archived,
});
const refs = (rows: NavigationSessionSummary[]) => rows.map((summary) => summary.ref);
const online = { connected: true, organizationReady: true };

it("applies each action to the sessions it can act on, once each", () => {
	const finished = row(local("a"));
	const working = row(local("b"), { state: "active" });
	const remote = row("paradise-park:c", { host_id: "paradise-park", session_id: "c" });
	const fork = row(local("d"), { kind: "fork" });
	const archivedAlready = row(local("e"), { state: "ended", live: false });
	const actions = selectionActions(
		[
			chosen(finished, "finished"),
			chosen(finished, "finished"),
			chosen(working, "working"),
			chosen(remote, "finished"),
			chosen(fork, "finished"),
			chosen(archivedAlready, "shutDown", true),
		],
		online,
	);
	expect(refs(actions.archive)).toEqual([local("a"), local("b"), "paradise-park:c"]);
	expect(refs(actions.pin)).toEqual([local("a"), local("b"), "paradise-park:c", local("e")]);
	expect(refs(actions.markRead)).toEqual([local("a"), "paradise-park:c", local("d")]);
});

it("keeps only Mark as read while a change is unresolved", () => {
	const selected = [chosen(row(local("a")), "finished")];
	const actions = selectionActions(selected, { connected: true, organizationReady: false });
	expect([actions.archive, actions.pin].map(refs)).toEqual([[], []]);
	expect(refs(actions.markRead)).toEqual([local("a")]);
});

it("keeps every action offline, to be held until the connection returns (phase 6 ruling 18)", () => {
	const selected = [chosen(row(local("a")), "finished")];
	const actions = selectionActions(selected, { connected: false, organizationReady: false });
	expect([actions.archive, actions.pin, actions.markRead].map(refs)).toEqual([
		[local("a")],
		[local("a")],
		[local("a")],
	]);
});

it("toggles one session in and out of the selection", () => {
	const once = toggleSelected(new Set(), local("a"));
	expect([...once]).toEqual([local("a")]);
	expect([...toggleSelected(once, local("a"))]).toEqual([]);
});

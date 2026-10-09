import type { NavigationSessionSummary } from "@evener/appwire-client";
import { expect, it } from "vitest";
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
const chosen = (summary: NavigationSessionSummary, archived = false) => ({
	item: { row: summary },
	archived,
});
const refs = (rows: NavigationSessionSummary[]) => rows.map((summary) => summary.ref);

it("applies each action to the sessions it can act on, once each", () => {
	const finished = row(local("a"));
	const working = row(local("b"), { state: "active" });
	const remote = row("paradise-park:c", { host_id: "paradise-park", session_id: "c" });
	const fork = row(local("d"), { kind: "fork" });
	const archivedAlready = row(local("e"), { state: "ended", live: false });
	const actions = selectionActions([
		chosen(finished),
		chosen(finished),
		chosen(working),
		chosen(remote),
		chosen(fork),
		chosen(archivedAlready, true),
	]);
	expect(refs(actions.archive)).toEqual([local("a"), local("b"), "paradise-park:c"]);
	expect(refs(actions.pin)).toEqual([local("a"), local("b"), "paradise-park:c", local("e")]);
});

it("offers Archive and Pin whatever the connection or the journal says, since a change that can't go now is held (phase 6)", () => {
	const selected = [chosen(row(local("a")))];
	const actions = selectionActions(selected);
	expect([actions.archive, actions.pin].map(refs)).toEqual([[local("a")], [local("a")]]);
});

it("toggles one session in and out of the selection", () => {
	const once = toggleSelected(new Set(), local("a"));
	expect([...once]).toEqual([local("a")]);
	expect([...toggleSelected(once, local("a"))]).toEqual([]);
});

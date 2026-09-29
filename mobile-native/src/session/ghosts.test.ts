import type { QueueState, ThreadCapabilities } from "@evener/appwire-client";
import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { describe, expect, it } from "vitest";
import {
	foldQueue,
	type GhostSource,
	ghostActionTarget,
	ghosts,
	type RecoveryGhostRow,
	shownGhosts,
	whatCanActNow,
} from "./ghosts";

const caps = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	queue: true,
	goal: true,
	sharedNotes: true,
	rename: true,
	...over,
});
const queue = (texts: string[], over: Partial<QueueState> = {}): QueueState => ({
	revision: 3,
	depth: texts.length,
	ids: texts.map((_, index) => `queue_${index + 1}`),
	texts,
	preview: texts.map((text) => text.split("\n")[0] ?? ""),
	...over,
});
const session = (type: string, texts: string[] = [], over: Partial<GhostSource> = {}): GhostSource => ({
	status: { type },
	capabilities: caps(),
	queue: queue(texts),
	...over,
});
const pending = (over: Partial<PendingTurnEntry> = {}): PendingTurnEntry => ({
	id: "cmid-1",
	ref: "ref-1",
	method: "send",
	text: "hello",
	imageCount: 0,
	skillNames: [],
	state: "submitting",
	source: "outbox",
	fromThisClient: true,
	...over,
});
const unsent = (text: string, sentText = text) => ({ text, sentText });
const row = (over: Partial<RecoveryGhostRow> = {}): RecoveryGhostRow => ({
	clientMutationId: "cmid-9",
	status: "rejected",
	reason: "daemon refused",
	text: "recover this message",
	actions: ["restore", "discard"],
	...over,
});

describe("queued messages (spec 8.5)", () => {
	it("lists the queue oldest first, with Steer now while the agent works", () => {
		const list = ghosts(session("active", ["first", "second"]), [], null, [], true);
		expect(list.map((ghost) => ghost.text)).toEqual(["first", "second"]);
		expect(list[0]).toMatchObject({
			state: "queued",
			caption: "Queued · sends when this turn ends",
			buttons: ["steerNow"],
			menu: ["edit", "cancel"],
			origin: { kind: "queue", entry: { index: 0, id: "queue_1" } },
		});
	});

	it("offers no Steer now to a harness that can't steer", () => {
		const [ghost] = ghosts(session("active", ["first"], { capabilities: caps({ steer: false }) }), [], null, [], true);
		expect(ghost?.buttons).toEqual([]);
		expect(ghost?.menu).toEqual(["edit", "cancel"]);
	});

	it("holds a queue a Stop parked, with Send now and Cancel", () => {
		const [ghost] = ghosts(session("idle", ["first"]), [], null, [], true);
		expect(ghost).toMatchObject({
			state: "held",
			caption: "Held · you stopped this turn",
			buttons: ["sendNow", "cancel"],
		});
	});

	it("treats a queue behind a question as queued, not held, and runs it next on its own", () => {
		const [ghost] = ghosts(session("awaiting", ["first"]), [], null, [], true);
		expect(ghost?.state).toBe("queued");
		expect(ghost?.buttons).toEqual([]);
	});

	it("offers no Send now for a parked queue a harness can't steer", () => {
		const [ghost] = ghosts(session("idle", ["first"], { capabilities: caps({ steer: false }) }), [], null, [], true);
		expect(ghost).toMatchObject({ state: "held", buttons: ["cancel"] });
	});

	it("can't act on an entry the daemon gave no id, or edit an image-only one", () => {
		const noIds = session("active", ["first"], { queue: queue(["first"], { ids: [] }) });
		expect(ghosts(noIds, [], null, [], true)[0]).toMatchObject({ buttons: [], menu: [] });
		const imageOnly = session("active", [""], { queue: queue([""], { preview: ["[image]"] }) });
		expect(ghosts(imageOnly, [], null, [], true)[0]).toMatchObject({ text: "[image]", menu: ["cancel"] });
	});
});

describe("messages on their way", () => {
	it("shows your steer until the agent picks it up, and your sends until they are reflected", () => {
		const list = ghosts(
			session("active", ["queued"]),
			[
				pending({ id: "a", method: "send", state: "submitting", text: "sent" }),
				pending({ id: "b", method: "promote", state: "accepted", text: "steered" }),
			],
			null,
			[],
			true,
		);
		expect(list.map((ghost) => [ghost.state, ghost.text])).toEqual([
			["steering", "steered"],
			["queued", "queued"],
			["sending", "sent"],
		]);
		expect(list[0]?.caption).toBe("Steering · arrives at the next step");
		expect(list[2]?.caption).toBe("Sending…");
	});

	it("asks you to check a send whose answer was lost", () => {
		const [ghost] = ghosts(session("idle"), [pending({ state: "blockedUnknown" })], null, [], true);
		expect(ghost).toMatchObject({
			state: "unconfirmed",
			caption: "Couldn't confirm this was sent",
			buttons: ["check", "discard"],
		});
	});
});

describe("messages that didn't make it (spec 14)", () => {
	it("keeps an unconfirmed send with Check and Discard, and Edit on tap", () => {
		const [ghost] = ghosts(session("idle"), [], unsent("maybe sent"), [], true);
		expect(ghost).toMatchObject({
			state: "unconfirmed",
			text: "maybe sent",
			buttons: ["check", "discard"],
			menu: ["edit"],
			origin: { kind: "draft" },
		});
	});

	it("shows one ghost for a send both the draft and the outbox hold, with the outbox's state", () => {
		// The draft keeps a send uncertain until the store confirms it, and the
		// outbox holds the same send once it is admitted: a binding change
		// between the two, or a crash, leaves both.
		const draft = unsent("look at [image 1]", "look at (attached image 1: a.png)");
		const held = (state: PendingTurnEntry["state"]) =>
			ghosts(
				session("idle"),
				[pending({ id: "a", text: "look at  (attached image 1: a.png)", imageCount: 1, state })],
				draft,
				[],
				true,
			);
		// Still on its way: nothing to do yet, like any send in flight. Discard
		// here would read as cancelling a send that will still land.
		expect(held("submitting")).toEqual([
			expect.objectContaining({
				state: "sending",
				text: "look at [image 1]",
				buttons: [],
				menu: [],
				// The ghost stands in for outbox row "a": its origin carries the id
				// so Discard can clear both.
				origin: { kind: "draft", clientMutationId: "a" },
			}),
		]);
		// The outbox lost track of it: the draft's actions are yours again.
		expect(held("blockedUnknown")).toEqual([
			expect.objectContaining({
				state: "unconfirmed",
				text: "look at [image 1]",
				buttons: ["check", "discard"],
				menu: ["edit"],
				origin: { kind: "draft", clientMutationId: "a" },
			}),
		]);
		// The outbox row is gone and the draft still doesn't know.
		expect(ghosts(session("idle"), [], draft, [], true)).toEqual([
			expect.objectContaining({ state: "unconfirmed", buttons: ["check", "discard"], menu: ["edit"] }),
		]);
	});

	it("binds the draft to its newest matching send, after an Edit and a resend of the same words", () => {
		// "ok" went out unconfirmed, Edit put it back, and it was sent again:
		// the draft's uncertainty is about the second send.
		const list = ghosts(
			session("idle"),
			[
				pending({ id: "first", text: "ok", state: "blockedUnknown", createdAt: 1 }),
				pending({ id: "second", text: " ok ", state: "submitting", createdAt: 2 }),
			],
			unsent("ok"),
			[],
			true,
		);
		expect(list.map((ghost) => [ghost.key, ghost.state])).toEqual([
			["pending:first", "unconfirmed"],
			["draft:unconfirmed", "sending"],
		]);
	});

	it("keeps a different message from the outbox as its own ghost", () => {
		const list = ghosts(
			session("idle"),
			[pending({ id: "a", text: "something else" })],
			unsent("maybe sent"),
			[],
			true,
		);
		expect(list.map((ghost) => ghost.text)).toEqual(["something else", "maybe sent"]);
	});

	it("says why the hub refused a message, and offers Edit only when it can come back", () => {
		expect(ghosts(session("idle"), [], null, [row()], true)[0]).toMatchObject({
			state: "refused",
			caption: "Couldn't send this · daemon refused",
			buttons: ["edit", "discard"],
		});
		expect(ghosts(session("idle"), [], null, [row({ actions: ["discard"] })], true)[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], null, [row({ reason: undefined })], true)[0]?.caption).toBe(
			"Couldn't send this",
		);
	});

	it("says why a refused message that carried an image can't come back, and only then", () => {
		const withImage = row({ actions: ["discard"], carriesAttachments: true });
		expect(ghosts(session("idle"), [], null, [withImage], true)[0]).toMatchObject({
			buttons: ["discard"],
			note: "This message carried an image, so it can't be restored to the draft here.",
		});
		expect(ghosts(session("idle"), [], null, [row()], true)[0]?.note).toBeUndefined();
		const orphanWithImage = row({ status: "orphaned", actions: ["discard"], carriesAttachments: true });
		expect(ghosts(session("idle"), [], null, [orphanWithImage], true)[0]?.note).toBeUndefined();
	});

	it("asks you to check a message the phone couldn't place", () => {
		expect(ghosts(session("idle"), [], null, [row({ status: "orphaned" })], true)[0]).toMatchObject({
			state: "unconfirmed",
			buttons: ["check", "discard"],
			menu: ["edit"],
		});
	});

	it("orders steers, the queue, sends, then everything that needs a look", () => {
		const list = ghosts(
			session("active", ["queued"]),
			[
				pending({ id: "u", state: "blockedUnknown", text: "lost" }),
				pending({ id: "s", method: "steer", state: "accepted", text: "steer" }),
				pending({ id: "q", method: "queue", state: "submitting", text: "sending" }),
			],
			unsent("draft"),
			[row()],
			true,
		);
		expect(list.map((ghost) => ghost.state)).toEqual([
			"steering",
			"queued",
			"unconfirmed",
			"sending",
			"unconfirmed",
			"refused",
		]);
	});
});

it("shows what the phone knows before the session has loaded", () => {
	expect(ghosts(null, [], unsent("maybe sent"), [row()], true).map((ghost) => ghost.state)).toEqual([
		"unconfirmed",
		"refused",
	]);
});

describe("acting on the message you saw (Review Focus 2)", () => {
	const live = queue(["a", "b", "c"]);
	it("acts on the same entry when it is still in place", () => {
		expect(ghostActionTarget(live, { index: 1, id: "queue_2" })).toEqual({ index: 1, id: "queue_2" });
	});
	it("follows an entry that moved, and refuses one that left", () => {
		expect(ghostActionTarget(queue(["b", "c"], { ids: ["queue_2", "queue_3"] }), { index: 1, id: "queue_2" })).toEqual({
			index: 0,
			id: "queue_2",
		});
		expect(ghostActionTarget(live, { index: 0, id: "queue_9" })).toBeNull();
		expect(ghostActionTarget(null, { index: 0, id: "queue_1" })).toBeNull();
		expect(ghostActionTarget(live, { index: 0, id: "" })).toBeNull();
	});
});

it("shows at most three queued messages and counts the rest, never hiding the others", () => {
	const all = ghosts(session("active", ["1", "2", "3", "4", "5"]), [], unsent("draft"), [], true);
	const { shown, moreQueued } = shownGhosts(all);
	expect(shown.map((ghost) => ghost.text)).toEqual(["1", "2", "3", "draft"]);
	expect(moreQueued).toBe(2);
});

// While you type, the queue folds to one line (spec 8.5): its count, and
// with one message, what you can do to it now.
describe("the queue folded while you type", () => {
	it("offers the one queued message's Steer now", () => {
		const all = ghosts(session("active", ["first"]), [], unsent("draft"), [], true);
		const { fold, rest } = foldQueue(all);
		expect(fold).toEqual({ label: "1 queued", act: { ghost: all[0], action: "steerNow" } });
		expect(rest.map((ghost) => ghost.text)).toEqual(["draft"]);
	});

	it("counts several, the whole queue, with nothing to act on until they show", () => {
		const all = ghosts(session("active", ["1", "2", "3", "4", "5"]), [], null, [], true);
		expect(foldQueue(all)).toEqual({ fold: { label: "5 queued", act: null }, rest: [] });
	});

	it("says a held queue is held, and offers the one message's Send now", () => {
		const all = ghosts(session("idle", ["first"]), [], null, [], true);
		expect(foldQueue(all).fold).toEqual({ label: "1 held", act: { ghost: all[0], action: "sendNow" } });
		const two = ghosts(session("idle", ["first", "second"]), [], null, [], true);
		expect(foldQueue(two).fold).toEqual({ label: "2 held", act: null });
	});

	it("offers nothing a harness can't do", () => {
		const all = ghosts(session("active", ["first"], { capabilities: caps({ steer: false }) }), [], null, [], true);
		expect(foldQueue(all).fold).toEqual({ label: "1 queued", act: null });
	});

	it("folds nothing when nothing is queued", () => {
		const all = ghosts(session("active"), [], unsent("draft"), [], true);
		expect(foldQueue(all)).toEqual({ fold: null, rest: all });
	});
});

describe("what can act right now", () => {
	const list = () => ghosts(session("idle", ["queued"]), [], unsent("maybe sent"), [row()], true);

	it("leaves everything when the hub is there and the composer has loaded", () => {
		expect(whatCanActNow(list(), { connected: true, composerLoaded: true })).toEqual(list());
	});

	it("takes a queued message's actions away while the hub is away, and nothing else's", () => {
		const [queued, draft, refused] = whatCanActNow(list(), { connected: false, composerLoaded: true });
		expect(queued).toMatchObject({ text: "queued", buttons: [], menu: [] });
		expect(draft?.buttons).toEqual(["check", "discard"]);
		expect(refused?.buttons).toEqual(["edit", "discard"]);
	});

	it("hides a queued message's Edit until the composer can take it", () => {
		const [queued] = whatCanActNow(list(), { connected: true, composerLoaded: false });
		expect(queued).toMatchObject({ buttons: ["sendNow", "cancel"], menu: ["cancel"] });
	});
});

describe("the phone's own outbox (phase 6)", () => {
	it("says a message will send when you're back online while offline", () => {
		const [ghost] = ghosts(session("idle"), [pending()], null, [], false);
		expect(ghost).toMatchObject({ state: "sending", caption: "Will send when you're back online", buttons: [] });
		expect(ghosts(session("idle"), [pending()], null, [], true)[0]?.caption).toBe("Sending…");
		// The draft's ghost for a send the outbox still carries says the same.
		const carried = [pending({ text: "maybe sent" })];
		expect(ghosts(session("idle"), carried, unsent("maybe sent"), [], false)).toMatchObject([
			{ key: "draft:unconfirmed", state: "sending", caption: "Will send when you're back online" },
		]);
	});

	it("leaves out another client's rows, and holds a message a Stop kept on the phone", () => {
		const list = ghosts(
			session("idle"),
			[pending({ id: "a", fromThisClient: false }), pending({ id: "b", state: "canceled", text: "held back" })],
			null,
			[],
			false,
		);
		expect(list).toEqual([
			{
				key: "pending:b",
				state: "held",
				text: "held back",
				caption: "Held · you stopped this turn",
				buttons: ["sendNow", "cancel"],
				menu: [],
				origin: { kind: "pending", clientMutationId: "b" },
			},
		]);
	});

	it("offers Check only while connected, and Discard on everything it couldn't confirm", () => {
		const lost = pending({ state: "blockedUnknown" });
		expect(ghosts(session("idle"), [lost], null, [], true)[0]?.buttons).toEqual(["check", "discard"]);
		expect(ghosts(session("idle"), [lost], null, [], false)[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], unsent("maybe sent"), [], false)[0]?.buttons).toEqual(["discard"]);
		expect(ghosts(session("idle"), [], null, [row({ status: "orphaned" })], false)[0]?.buttons).toEqual(["discard"]);
	});

	it("never lets a message a Stop held stand in for an unconfirmed draft's send", () => {
		const list = ghosts(
			session("idle"),
			[pending({ id: "b", state: "canceled", text: "maybe sent" })],
			unsent("maybe sent"),
			[],
			true,
		);
		expect(list.map((ghost) => [ghost.key, ghost.state])).toEqual([
			["pending:b", "held"],
			["draft:unconfirmed", "unconfirmed"],
		]);
	});
});

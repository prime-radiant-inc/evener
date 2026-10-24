import { describe, expect, it } from "vitest";
import { canonicalFailureAction, errorAction } from "./errorAction";

const turns = [{ id: "turn_1" }, { id: "turn_2" }] as never;
const failure = (detail: string, turnId = "turn_2", title = "The turn failed") => ({
	id: `failure:${turnId}`,
	title,
	detail,
	turnId,
});
const retry = (disabled = false) => ({ kind: "retry", failedTurnId: "turn_2", disabled });

/** The shared store's Retry inputs: the canonical model only while ready, the
 * displayed model's last turn as the fallback identity while it is not, and
 * the durable pending projection plus its readiness. */
function state(overrides: Record<string, unknown> = {}) {
	return {
		canonical: { turns, status: { type: "idle" } },
		displayedLatestTurnId: "turn_2",
		pendingMutations: [],
		pendingMutationsReady: true,
		...overrides,
	} as never;
}

function entry(overrides: Record<string, unknown> = {}) {
	return {
		id: "entry-1",
		ref: "ref",
		method: "send",
		text: "continue",
		imageCount: 0,
		skillNames: [],
		state: "submitting",
		source: "outbox",
		fromThisClient: true,
		...overrides,
	} as never;
}

describe("an error's one action (spec 8.2, ruling 26)", () => {
	it.each([
		["the canonical latest turn's own failure retries", failure("go test exited 1"), state(), true, retry()],
		[
			"a fenced session's failure retries through Send",
			failure("go test exited 1"),
			state({ canonical: { turns, status: { type: "idle" }, resumeRequired: true } }),
			true,
			retry(),
		],
		["a historical failure is hidden", failure("go test exited 1", "turn_1"), state(), true, null],
		[
			"another turn running hides it",
			failure("go test exited 1"),
			state({ canonical: { turns, status: { type: "active" }, runningTurnId: "turn_3" } }),
			true,
			null,
		],
		[
			"active without a running identity hides it",
			failure("go test exited 1"),
			state({ canonical: { turns, status: { type: "active" } } }),
			true,
			null,
		],
		[
			"invalidated history hides it",
			failure("go test exited 1"),
			state({ canonical: { turns, status: { type: "idle" }, history: { invalidatedAtGeneration: 2 } } }),
			true,
			null,
		],
		[
			"failed history hides it",
			failure("go test exited 1"),
			state({ canonical: { turns, status: { type: "idle" }, history: { failed: "history read failed" } } }),
			true,
			null,
		],
		["no retry while Send can't act", failure("go test exited 1"), state(), false, null],
		[
			"no retry while Send can't act even with pending input",
			failure("go test exited 1"),
			state({ pendingMutations: [entry()], pendingMutationsReady: false }),
			false,
			null,
		],
		["no retry for a warning's row", { ...failure("rate limited"), id: "warn-item-1" }, state(), true, null],
		[
			"the last displayed failure stays, disabled, while canonical authority is unknown",
			failure("go test exited 1"),
			state({ canonical: null }),
			true,
			retry(true),
		],
		[
			"an older displayed failure is hidden while canonical authority is unknown",
			failure("go test exited 1", "turn_1"),
			state({ canonical: null }),
			true,
			null,
		],
	] as const)("%s", (_name, row, model, canSend, expected) => {
		expect(errorAction(row, model, canSend)).toEqual(expected);
	});

	it.each([
		["pending readiness unknown", state({ pendingMutationsReady: false, pendingMutations: null }), true],
		["an own send", state({ pendingMutations: [entry({ method: "send" })] }), true],
		["an own queue", state({ pendingMutations: [entry({ method: "queue" })] }), true],
		["empty pending rows", state({ pendingMutations: [] }), false],
		["a foreign send", state({ pendingMutations: [entry({ fromThisClient: false })] }), false],
		["a foreign queue", state({ pendingMutations: [entry({ method: "queue", fromThisClient: false })] }), false],
		["a canceled own send", state({ pendingMutations: [entry({ state: "canceled" })] }), false],
		["a canceled own queue", state({ pendingMutations: [entry({ method: "queue", state: "canceled" })] }), false],
		["an own steer", state({ pendingMutations: [entry({ method: "steer" })] }), false],
	] as const)("%s sets disabled %s", (_name, model, disabled) => {
		expect(errorAction(failure("go test exited 1"), model, true)).toEqual(retry(disabled));
	});

	it("keeps the clicked turn identity in the descriptor", () => {
		expect(errorAction(failure("go test exited 1"), state(), true)).toMatchObject({
			kind: "retry",
			failedTurnId: "turn_2",
		});
	});

	it.each([
		["an expired sign-in", failure("Sign-in expired")],
		["a 401", failure("401 Unauthorized")],
		["invalid credentials, either word order", failure("Invalid credentials")],
		["expired credentials, either word order", failure("Expired credential")],
	] as const)("%s signs in even while Retry is pending or unready", (_name, row) => {
		expect(errorAction(row, state({ canonical: null, pendingMutationsReady: false }), false)).toEqual({
			kind: "signIn",
		});
	});
});

describe("activation resolves the current failure row", () => {
	const failureRow = (title = "The turn failed", detail = "go test exited 1") => ({
		kind: "failure",
		id: "failure:turn_2",
		title,
		detail,
		turnId: "turn_2",
	});
	const canonical = (items: unknown[]) => ({ turns, status: { type: "idle" }, items }) as never;

	it("offers Retry for the current canonical own failure", () => {
		expect(canonicalFailureAction(canonical([failureRow()]), "turn_2", [], true, true)).toEqual({
			kind: "retry",
			failedTurnId: "turn_2",
			disabled: false,
		});
	});

	it("returns Sign in when the same turn's error now requires it", () => {
		expect(canonicalFailureAction(canonical([failureRow("Sign-in expired", "")]), "turn_2", [], true, true)).toEqual({
			kind: "signIn",
		});
	});

	it("returns nothing when the turn no longer has a failure row", () => {
		expect(canonicalFailureAction(canonical([]), "turn_2", [], true, true)).toBeNull();
	});

	it("returns nothing without canonical authority", () => {
		expect(canonicalFailureAction(null, "turn_2", [], true, true)).toBeNull();
	});
});

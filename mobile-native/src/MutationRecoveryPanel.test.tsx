// The recovery-panel contract: the surface that enumerates one exact
// composite target's durable recovery rows (the landed slice-3 projection),
// offers a restore of a rejected row's text and an exact-target discard of a
// single row. The projection, the action gate and the discard wrapper are
// pure; the rows show as ghosts at the transcript's end (session/ghosts.ts and
// session/QueuedMessages.test.tsx), and the failure line renders through the
// shared native testkit so a green run is proof about the real component.

import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
	MutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";
import {
	discardRecoveredMutation,
	nativeMutationRecoveryActions,
	projectNativeMutationRecovery,
	recordCarriesAttachments,
	recoveredComposerText,
	RecoveryFailure,
	recoveryFailureMessage,
	type RecoveryPanelSurface,
	useRecoveryPanel,
} from "./MutationRecoveryPanel";
import { render, renderedText, renderHook } from "./renderNative.testkit";
import type { NativeMutationRecoveryRuntime } from "./useNativeMutationRecovery";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
// The consumer hook module reaches the runtime's native edges at import time;
// the tests never acquire a real runtime (they inject `acquire`), so inert
// mocks are enough to make the module load.
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

const TARGET_A = JSON.stringify(["hub-a", "ref-a"]);
const TARGET_B = JSON.stringify(["hub-b", "ref-b"]);

function recovery(
	targetRef: string,
	clientMutationId: string,
	intentSequence: number,
	kind: "rejected" | "orphaned",
	extra: Partial<MutationRecoveryRecord<MutationAttachmentRef>> = {},
): MutationRecoveryRecord<MutationAttachmentRef> {
	return {
		version: 1,
		clientMutationId,
		targetRef,
		threadId: "thread",
		method: "turn/start",
		payload: { input: [{ type: "text", text: clientMutationId }] },
		attachments: [],
		optimisticDisplay: {},
		intentSequence,
		createdAt: intentSequence,
		state: "blockedUnknown",
		composerText: clientMutationId,
		recoveryKind: kind,
		...extra,
	};
}

function snapshot(
	records: MutationRecoveryRecord<MutationAttachmentRef>[],
): MutationPersistenceSnapshot<MutationAttachmentRef> {
	return { outbox: [], optimistic: [], recovery: records };
}

// The rows the surface's read shows for its own target, as the screen
// projects them.
function recoveryRowCount(surface: RecoveryPanelSurface): number {
	return projectNativeMutationRecovery(surface.targetKey, surface.snapshot).length;
}

it("projects only the exact target's recovery rows, in intent order", () => {
	const rows = projectNativeMutationRecovery(
		TARGET_A,
		snapshot([
			recovery(TARGET_A, "second", 2, "orphaned"),
			recovery(TARGET_B, "other", 1, "rejected"),
			recovery(TARGET_A, "first", 1, "rejected"),
		]),
	);

	expect(rows.map((row) => row.clientMutationId)).toEqual(["first", "second"]);
	expect(rows.map((row) => row.targetKey)).toEqual([TARGET_A, TARGET_A]);
	expect(rows.map((row) => row.status)).toEqual(["rejected", "orphaned"]);
});

it("offers restore only for an eligible rejected record", () => {
	const eligible = recovery(TARGET_A, "eligible", 1, "rejected");
	expect(nativeMutationRecoveryActions(eligible)).toEqual(["restore", "discard"]);
	// No text a restore could write.
	expect(
		nativeMutationRecoveryActions(
			recovery(TARGET_A, "no-text", 1, "rejected", {
				composerText: undefined,
				payload: { input: [] },
			}),
		),
	).toEqual(["discard"]);
	// An attachment a text-only restore would silently drop.
	expect(
		nativeMutationRecoveryActions(
			recovery(TARGET_A, "image", 1, "rejected", {
				payload: {
					input: [{ type: "image", mediaType: "image/png", data: "AQID" }],
				},
			}),
		),
	).toEqual(["discard"]);
	// An orphaned record has no daemon message to replay.
	expect(nativeMutationRecoveryActions(recovery(TARGET_A, "orphan", 1, "orphaned"))).toEqual(["discard"]);
});

it("carries restoreOffered from the record alone, and the row's actions agree with it", () => {
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([recovery(TARGET_A, "rejected", 1, "rejected")]));
	// The record offers Restore, so the row reports restoreOffered and carries
	// the restore action. Whether the composer can take it right now is the
	// ghost's canEdit, decided at render time, not a projection input.
	expect(rows[0].restoreOffered).toBe(true);
	expect(rows[0].actions).toEqual(["restore", "discard"]);
});

it("offers an ordinary eligible rejected record restore and discard", () => {
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([recovery(TARGET_A, "rejected", 1, "rejected")]));
	expect(rows[0].actions).toEqual(["restore", "discard"]);
});

it("never offers restore for a rejected interrupt, even when it somehow carries text", () => {
	const interrupt = recovery(TARGET_A, "interrupt", 1, "rejected", {
		method: "turn/interrupt",
		composerText: "stop",
	});
	expect(nativeMutationRecoveryActions(interrupt)).toEqual(["discard"]);
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([interrupt]));
	expect(rows[0].actions).toEqual(["discard"]);
});

it("reports the payload's text when a record carries no composer text", () => {
	expect(
		recoveredComposerText(
			recovery(TARGET_A, "no-composer", 1, "rejected", {
				composerText: undefined,
				payload: {
					input: [
						{ type: "text", text: "one" },
						{ type: "image", mediaType: "image/png", data: "AQID" },
						{ type: "text", text: "two" },
					],
				},
			}),
		),
	).toBe("one\ntwo");
});

it("discards exactly the row's clientMutationId through the target's own projection", async () => {
	const discard = vi.fn(async () => true);
	const projection = { targetKey: TARGET_A, discard };
	const row = projectNativeMutationRecovery(TARGET_A, snapshot([recovery(TARGET_A, "row-1", 1, "rejected")]))[0];

	expect(await discardRecoveredMutation(projection, row)).toBe(true);
	expect(discard).toHaveBeenCalledTimes(1);
	expect(discard).toHaveBeenCalledWith("row-1");
});

it("refuses to discard a row another target owns, so a discard is never blanket", async () => {
	const discard = vi.fn(async () => true);
	const foreign = projectNativeMutationRecovery(TARGET_B, snapshot([recovery(TARGET_B, "foreign", 1, "rejected")]))[0];

	expect(await discardRecoveredMutation({ targetKey: TARGET_A, discard }, foreign)).toBe(false);
	expect(discard).not.toHaveBeenCalled();
});

it("surfaces a read error with its own words", () => {
	const tree = render(<RecoveryFailure error={new Error("recovery table unavailable")} onRetry={() => {}} />);
	expect(renderedText(tree)).toContain("recovery table unavailable");
});

function fakeRuntime(overrides: Partial<NativeMutationRecoveryRuntime> = {}): NativeMutationRecoveryRuntime {
	return {
		read: async (targetKey) => ({
			outbox: [],
			optimistic: [],
			recovery: [recovery(targetKey, "row-1", 1, "rejected")],
		}),
		subscribeStorage: () => () => {},
		discardRecovery: async () => true,
		...overrides,
	};
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

it("counts no rows while the snapshot is empty and counts them when rows arrive", async () => {
	const targetKey = JSON.stringify(["hub-a", "ref-a"]);
	let rows: MutationRecoveryRecord<MutationAttachmentRef>[] = [];
	const runtime = fakeRuntime({
		read: async () => ({ outbox: [], optimistic: [], recovery: rows }),
	});
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	expect(recoveryRowCount(result.current)).toBe(0);

	rows = [recovery(targetKey, "row-1", 1, "rejected")];
	act(() => result.current.retry());
	await flush();
	expect(recoveryRowCount(result.current)).toBe(1);
});

it("surfaces a failed runtime acquisition and clears it on retry", async () => {
	let mode: "fail" | "ok" = "fail";
	const acquire = () => {
		if (mode === "fail") throw new Error("mutations db unavailable");
		return fakeRuntime();
	};
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire,
		}),
	);
	await flush();

	expect(result.current.error).toBeInstanceOf(Error);
	expect(result.current.failed).toBe(true);
	expect(recoveryFailureMessage(result.current.error)).toBe("mutations db unavailable");
	expect(recoveryRowCount(result.current)).toBe(0);

	mode = "ok";
	act(() => result.current.retry());
	await flush();

	expect(result.current.error).toBeNull();
	expect(result.current.failed).toBe(false);
	expect(recoveryRowCount(result.current)).toBe(1);
});

it("surfaces a rejected discard instead of leaving an unhandled rejection", async () => {
	const runtime = fakeRuntime({
		discardRecovery: async () => {
			throw new Error("discard failed");
		},
	});
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	const row = projectNativeMutationRecovery(result.current.targetKey, result.current.snapshot)[0];

	act(() => {
		result.current.discard(row);
	});
	await flush();

	expect(result.current.error).toBeInstanceOf(Error);
	expect(recoveryFailureMessage(result.current.error)).toBe("discard failed");
});

it("leaves no failure after a successful discard", async () => {
	const runtime = fakeRuntime();
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	const row = projectNativeMutationRecovery(result.current.targetKey, result.current.snapshot)[0];

	act(() => {
		result.current.discard(row);
	});
	await flush();

	expect(result.current.error).toBeNull();
});

it("offers a retry beside a surfaced error", () => {
	const onRetry = vi.fn();
	const tree = render(<RecoveryFailure error={new Error("read failed")} onRetry={onRetry} />);
	const retry = tree.root.findByProps({ accessibilityLabel: "Retry" });
	act(() => retry.props.onPress());
	expect(onRetry).toHaveBeenCalledOnce();
});

it("refreshes a failed read on retry", async () => {
	let failing = true;
	const runtime = fakeRuntime({
		read: async (targetKey) => {
			if (failing) throw new Error("read failed");
			return {
				outbox: [],
				optimistic: [],
				recovery: [recovery(targetKey, "row-1", 1, "rejected")],
			};
		},
	});
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	expect(result.current.error).toBeInstanceOf(Error);
	expect(result.current.failed).toBe(true);
	expect(recoveryRowCount(result.current)).toBe(0);

	failing = false;
	act(() => result.current.retry());
	await flush();

	expect(result.current.error).toBeNull();
	expect(recoveryRowCount(result.current)).toBe(1);
});

it("shows a failure with no rows, and its Retry is reachable", () => {
	const onRetry = vi.fn();
	const tree = render(<RecoveryFailure error={new Error("mutations db unavailable")} onRetry={onRetry} />);
	expect(renderedText(tree)).toContain("mutations db unavailable");
	const retry = tree.root.findByProps({ accessibilityLabel: "Retry" });
	act(() => retry.props.onPress());
	expect(onRetry).toHaveBeenCalledOnce();
});

it("treats a record with attachment metadata as carrying attachments even without a payload image item", () => {
	const record = recovery(TARGET_A, "meta-attachment", 1, "rejected", {
		composerText: "look [image 1]",
		attachments: [
			{
				presentationId: "presentation-1",
				marker: 1,
				name: "proof.png",
				mediaType: "image/png",
			},
		],
		payload: { input: [{ type: "text", text: "look [image 1]" }] },
	});
	expect(recordCarriesAttachments(record)).toBe(true);
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([record]));
	expect(rows[0].carriesAttachments).toBe(true);
	expect(rows[0].actions).toEqual(["discard"]);
});

it("withholds restore for a rejected record that carries an image, which the ghost explains", () => {
	const record = recovery(TARGET_A, "with-image", 1, "rejected", {
		payload: {
			input: [
				{ type: "text", text: "look [image 1]" },
				{
					type: "image",
					mediaType: "image/png",
					data: "AQID",
					name: "proof.png",
				},
			],
		},
		composerText: "look [image 1]",
	});
	expect(recordCarriesAttachments(record)).toBe(true);
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([record]));
	expect(rows[0].carriesAttachments).toBe(true);
	expect(rows[0].actions).toEqual(["discard"]);
});

it("clears a stale acquisition failure when a later acquisition succeeds", async () => {
	let calls = 0;
	const runtime = fakeRuntime();
	const acquire = () => {
		calls += 1;
		if (calls === 1) throw new Error("mutations db unavailable");
		return runtime;
	};
	let connected = true;
	const { result, rerender } = renderHook(() =>
		useRecoveryPanel({
			connected,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire,
		}),
	);
	await flush();
	expect(result.current.error).toBeInstanceOf(Error);

	connected = false;
	rerender();
	await flush();
	connected = true;
	rerender();
	await flush();

	expect(result.current.error).toBeNull();
	expect(recoveryRowCount(result.current)).toBe(1);
});

it("clears an in-scope discard failure after a later successful discard", async () => {
	let fail = true;
	const runtime = fakeRuntime({
		discardRecovery: async () => {
			if (fail) throw new Error("discard failed");
			return true;
		},
	});
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	const row = projectNativeMutationRecovery(result.current.targetKey, result.current.snapshot)[0];
	act(() => {
		result.current.discard(row);
	});
	await flush();
	expect(result.current.error).toBeInstanceOf(Error);

	fail = false;
	act(() => {
		result.current.discard(row);
	});
	await flush();
	expect(result.current.error).toBeNull();
});

it("scopes a discard failure to its target, so switching targets shows the new target's rows", async () => {
	const runtime = fakeRuntime({
		discardRecovery: async () => {
			throw new Error("discard failed");
		},
	});
	let hubId = "hub-a";
	let targetRef = "ref-a";
	const { result, rerender } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId,
			targetRef,
			acquire: () => runtime,
		}),
	);
	await flush();
	const row = projectNativeMutationRecovery(result.current.targetKey, result.current.snapshot)[0];
	act(() => {
		result.current.discard(row);
	});
	await flush();
	expect(result.current.error).toBeInstanceOf(Error);

	hubId = "hub-b";
	targetRef = "ref-b";
	rerender();
	await flush();

	expect(result.current.error).toBeNull();
	expect(recoveryRowCount(result.current)).toBe(1);
});

it("withholds restore for an image input carried by path or metadata without inline data", () => {
	const record = recovery(TARGET_A, "path-image", 1, "rejected", {
		composerText: "look [image 1]",
		attachments: [],
		payload: {
			input: [
				{ type: "text", text: "look [image 1]" },
				{ type: "image", path: "file:///proof.png", mediaType: "image/png" },
			],
		},
	});
	expect(recordCarriesAttachments(record)).toBe(true);
	const rows = projectNativeMutationRecovery(TARGET_A, snapshot([record]));
	expect(rows[0].carriesAttachments).toBe(true);
	expect(rows[0].actions).toEqual(["discard"]);
});

it("does not resurface a stale acquisition failure after a target round-trip", async () => {
	const runtime = fakeRuntime();
	let targetRef = "ref-a";
	const acquire = () => {
		if (targetRef === "ref-a") throw new Error("mutations db unavailable");
		return runtime;
	};
	const { result, rerender } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef,
			acquire,
		}),
	);
	await flush();
	expect(result.current.error).toBeInstanceOf(Error);

	targetRef = "ref-b";
	rerender();
	await flush();
	expect(result.current.error).toBeNull();

	targetRef = "ref-a";
	rerender();
	await flush();
	expect(result.current.error).toBeNull();
	expect(result.current.failed).toBe(false);
});

it("ignores a stale discard rejection that lands after a newer discard", async () => {
	let rejectFirst: (error: unknown) => void = () => {};
	const first = new Promise<boolean>((_resolve, reject) => {
		rejectFirst = reject;
	});
	let calls = 0;
	const runtime = fakeRuntime({
		discardRecovery: () => {
			calls += 1;
			return calls === 1 ? first : Promise.resolve(true);
		},
	});
	const { result } = renderHook(() =>
		useRecoveryPanel({
			connected: true,
			hubId: "hub-a",
			targetRef: "ref-a",
			acquire: () => runtime,
		}),
	);
	await flush();
	const row = projectNativeMutationRecovery(result.current.targetKey, result.current.snapshot)[0];
	act(() => {
		result.current.discard(row);
	});
	act(() => {
		result.current.discard(row);
	});
	await flush();
	expect(result.current.error).toBeNull();

	await act(async () => {
		rejectFirst(new Error("stale discard failed"));
		await Promise.resolve();
	});
	await flush();
	expect(result.current.error).toBeNull();
});

it("returns a string error's own message", () => {
	expect(recoveryFailureMessage("boom")).toBe("boom");
});

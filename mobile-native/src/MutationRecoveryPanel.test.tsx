// The recovery-panel contract: the surface that enumerates one exact
// composite target's durable recovery rows (the landed slice-3 projection),
// offers a restore of a rejected row's text and an exact-target discard of a
// single row. The projection, the action gate and the discard wrapper are
// pure; the component is rendered through the shared native testkit so a
// green run is proof about the real component, not a stub.

import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
	MutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";
import {
	discardRecoveredMutation,
	MutationRecoveryPanel,
	nativeMutationRecoveryActions,
	projectNativeMutationRecovery,
	recordCarriesAttachments,
	recoveredComposerText,
	recoveryFailureMessage,
	shouldOfferRecoveryEntry,
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

const noActions = {
	canRestore: () => true,
	onRestore: () => {},
	onDiscard: () => {},
};

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

it("offers restore only for a rejected row that carries restorable text, and never for an orphaned row", () => {
	expect(nativeMutationRecoveryActions("rejected", true)).toEqual([
		"restore",
		"discard",
	]);
	expect(nativeMutationRecoveryActions("rejected", false)).toEqual(["discard"]);
	expect(nativeMutationRecoveryActions("rejected", true, true)).toEqual([
		"discard",
	]);
	expect(nativeMutationRecoveryActions("orphaned", true)).toEqual(["discard"]);
	expect(nativeMutationRecoveryActions("orphaned", false)).toEqual(["discard"]);
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

it("renders each row's reason and text with only its approved action labels", () => {
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([
			recovery(TARGET_A, "rejected", 1, "rejected", {
				recoveryReason: "daemon refused",
				composerText: "hello there",
			}),
			recovery(TARGET_A, "orphaned", 2, "orphaned", { composerText: undefined }),
		]),
		error: null as unknown,
		actions: noActions,
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);

	const labels = tree.root
		.findAllByProps({ accessibilityRole: "button" })
		.map((button) => button.props.accessibilityLabel);
	expect(labels).toEqual(["Restore to draft", "Discard", "Discard"]);

	const text = renderedText(tree);
	expect(text).toContain("Rejected");
	expect(text).toContain("daemon refused");
	expect(text).toContain("hello there");
	expect(text).toContain("Needs review");
});

it("invokes typed restore and discard callbacks for the rows they belong to", () => {
	const onRestore = vi.fn();
	const onDiscard = vi.fn();
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([
			recovery(TARGET_A, "rejected", 1, "rejected"),
			recovery(TARGET_A, "orphaned", 2, "orphaned"),
		]),
		error: null as unknown,
		actions: { canRestore: () => true, onRestore, onDiscard },
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	const buttons = tree.root.findAllByProps({ accessibilityRole: "button" });

	act(() => {
		buttons[0]?.props.onPress();
		buttons[1]?.props.onPress();
	});

	expect(onRestore).toHaveBeenCalledTimes(1);
	expect(onRestore).toHaveBeenCalledWith(
		expect.objectContaining({ clientMutationId: "rejected", targetKey: TARGET_A }),
	);
	expect(onDiscard).toHaveBeenCalledWith(
		expect.objectContaining({ clientMutationId: "rejected", targetKey: TARGET_A }),
	);
});

it("discards exactly the row's clientMutationId through the target's own projection", async () => {
	const discard = vi.fn(async () => true);
	const projection = { targetKey: TARGET_A, discard };
	const row = projectNativeMutationRecovery(
		TARGET_A,
		snapshot([recovery(TARGET_A, "row-1", 1, "rejected")]),
	)[0];

	expect(await discardRecoveredMutation(projection, row)).toBe(true);
	expect(discard).toHaveBeenCalledTimes(1);
	expect(discard).toHaveBeenCalledWith("row-1");
});

it("refuses to discard a row another target owns, so a discard is never blanket", async () => {
	const discard = vi.fn(async () => true);
	const foreign = projectNativeMutationRecovery(
		TARGET_B,
		snapshot([recovery(TARGET_B, "foreign", 1, "rejected")]),
	)[0];

	expect(
		await discardRecoveredMutation({ targetKey: TARGET_A, discard }, foreign),
	).toBe(false);
	expect(discard).not.toHaveBeenCalled();
});

it("renders a loading line until the snapshot arrives and an empty line when nothing is recoverable", () => {
	const loading = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={null}
			error={null}
			loading
			actions={noActions}
		/>,
	);
	expect(renderedText(loading)).toContain("Loading delivery status");

	const unavailable = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={null}
			error={null}
			loading={false}
			actions={noActions}
		/>,
	);
	expect(renderedText(unavailable)).toContain("Recovery is unavailable");
	expect(renderedText(unavailable)).not.toContain("Loading delivery status");

	const empty = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={snapshot([])}
			error={null}
			actions={noActions}
		/>,
	);
	expect(renderedText(empty)).toContain("No messages need recovery");
});

it("surfaces a read error instead of a silent empty panel", () => {
	const tree = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={null}
			error={new Error("recovery table unavailable")}
			actions={noActions}
		/>,
	);
	expect(renderedText(tree)).toContain("recovery table unavailable");
});

function fakeRuntime(
	overrides: Partial<NativeMutationRecoveryRuntime> = {},
): NativeMutationRecoveryRuntime {
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

it("offers the recovery entry for rows, and for a failed surface so its error and retry stay reachable", () => {
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: 1,
			failed: false,
		}),
	).toBe(true);
	// Empty snapshot + no failure = no entry: no dead affordance ships.
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: 0,
			failed: false,
		}),
	).toBe(false);
	// A failure renders the entry even with no rows, so the modal's error and
	// Retry are reachable instead of hidden behind an entry that never appears.
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: 0,
			failed: true,
		}),
	).toBe(true);
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: true,
			count: 3,
			failed: false,
		}),
	).toBe(false);
	expect(
		shouldOfferRecoveryEntry({
			connected: false,
			deliveryConcern: false,
			count: 3,
			failed: true,
		}),
	).toBe(false);
});

it("hides the entry while the snapshot is empty and shows it when rows arrive", async () => {
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
	expect(result.current.count).toBe(0);
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: result.current.count,
			failed: result.current.failed,
		}),
	).toBe(false);

	rows = [recovery(targetKey, "row-1", 1, "rejected")];
	act(() => result.current.retry());
	await flush();
	expect(result.current.count).toBe(1);
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: result.current.count,
			failed: result.current.failed,
		}),
	).toBe(true);
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
	expect(recoveryFailureMessage(result.current.error)).toBe(
		"mutations db unavailable",
	);
	expect(result.current.count).toBe(0);
	// A failure with no rows still renders the entry, so the modal's error and
	// Retry are reachable rather than hidden behind an entry that never appears.
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: result.current.count,
			failed: result.current.failed,
		}),
	).toBe(true);

	mode = "ok";
	act(() => result.current.retry());
	await flush();

	expect(result.current.error).toBeNull();
	expect(result.current.failed).toBe(false);
	expect(result.current.count).toBe(1);
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: result.current.count,
			failed: result.current.failed,
		}),
	).toBe(true);
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
	const row = projectNativeMutationRecovery(
		result.current.targetKey,
		result.current.snapshot,
	)[0];

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
	const row = projectNativeMutationRecovery(
		result.current.targetKey,
		result.current.snapshot,
	)[0];

	act(() => {
		result.current.discard(row);
	});
	await flush();

	expect(result.current.error).toBeNull();
});

it("disables restore with an explanation when the composer cannot accept it", () => {
	const onRestore = vi.fn();
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([recovery(TARGET_A, "rejected", 1, "rejected")]),
		error: null as unknown,
		actions: { canRestore: () => false, onRestore, onDiscard: () => {} },
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	const restore = tree.root.findByProps({
		accessibilityLabel: "Restore to draft",
	});

	expect(restore.props.accessibilityState).toMatchObject({ disabled: true });
	expect(renderedText(tree)).toContain(
		"Clear or send your current draft to restore this message.",
	);
	act(() => restore.props.onPress());
	expect(onRestore).not.toHaveBeenCalled();
});

it("offers a retry beside a surfaced error", () => {
	const onRetry = vi.fn();
	const tree = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={null}
			error={new Error("read failed")}
			onRetry={onRetry}
			actions={noActions}
		/>,
	);
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
	expect(result.current.count).toBe(0);

	failing = false;
	act(() => result.current.retry());
	await flush();

	expect(result.current.error).toBeNull();
	expect(result.current.count).toBe(1);
});

it("renders a reachable entry on a failure with no rows, and its Retry is reachable", () => {
	expect(
		shouldOfferRecoveryEntry({
			connected: true,
			deliveryConcern: false,
			count: 0,
			failed: true,
		}),
	).toBe(true);

	const onRetry = vi.fn();
	const tree = render(
		<MutationRecoveryPanel
			targetKey={TARGET_A}
			snapshot={null}
			error={new Error("mutations db unavailable")}
			onRetry={onRetry}
			actions={noActions}
		/>,
	);
	expect(renderedText(tree)).toContain("mutations db unavailable");
	const retry = tree.root.findByProps({ accessibilityLabel: "Retry" });
	act(() => retry.props.onPress());
	expect(onRetry).toHaveBeenCalledOnce();
});

it("does not put an image-specific explanation on an orphaned row", () => {
	const record = recovery(TARGET_A, "orphan-img", 1, "orphaned", {
		attachments: [
			{
				presentationId: "presentation-1",
				marker: 1,
				name: "proof.png",
				mediaType: "image/png",
			},
		],
		payload: { input: [{ type: "text", text: "hi" }] },
	});
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([record]),
		error: null as unknown,
		actions: noActions,
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	expect(renderedText(tree)).not.toContain("carried an image");
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

it("withholds restore for a rejected record that carries an image, and says why", () => {
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

	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([record]),
		error: null as unknown,
		actions: noActions,
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	expect(
		tree.root.findAllByProps({ accessibilityLabel: "Restore to draft" }),
	).toHaveLength(0);
	expect(renderedText(tree)).toContain("carried an image");
});

it("renders a failure banner above the rows instead of hiding them", () => {
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([
			recovery(TARGET_A, "rejected", 1, "rejected", {
				composerText: "hello there",
			}),
		]),
		error: new Error("discard failed"),
		actions: noActions,
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	const text = renderedText(tree);
	expect(text).toContain("discard failed");
	expect(text).toContain("hello there");
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
	expect(result.current.count).toBe(1);
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
	const row = projectNativeMutationRecovery(
		result.current.targetKey,
		result.current.snapshot,
	)[0];
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
	const row = projectNativeMutationRecovery(
		result.current.targetKey,
		result.current.snapshot,
	)[0];
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
	expect(result.current.count).toBe(1);
});

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
	recoveredComposerText,
} from "./MutationRecoveryPanel";
import { render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

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

const noActions = { onRestore: () => {}, onDiscard: () => {} };

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
		actions: { onRestore, onDiscard },
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
			actions={noActions}
		/>,
	);
	expect(renderedText(loading)).toContain("Loading delivery status");

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

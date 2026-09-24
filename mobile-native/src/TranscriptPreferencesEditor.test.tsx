// The transcript screen's recovery from a present-but-unreadable draft record:
// the shared store classifies it (draftUnreadable + storageUnavailable, no
// draft), and the store's own discard gate allows exactly that record to be
// thrown away. The editor must therefore render the discard action even though
// there is no draft to show, or an upgrading user whose stored checkpoint no
// build can decode is stuck: editing is disabled and nothing offers the one
// recovery the store permits.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { NativePreferencesSnapshot } from "./nativePreferences";
import { TranscriptPreferencesEditor } from "./TranscriptPreferencesEditor";
import { render } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

const unreadable: NativePreferencesSnapshot["transcriptMobile"] = {
	support: "supported",
	loading: false,
	saving: false,
	confirmed: null,
	draft: null,
	error: "Could not restore the saved transcript draft. Check current settings to retry.",
	conflict: false,
	writeUncertain: false,
	storageUnavailable: true,
	draftUnreadable: true,
};

function editor(
	state: NativePreferencesSnapshot["transcriptMobile"],
	discard: () => void,
) {
	return render(
		<TranscriptPreferencesEditor
			hubName="Work hub"
			state={state}
			connected
			edit={() => {}}
			save={() => {}}
			refresh={() => {}}
			discard={discard}
			rebase={() => {}}
		/>,
	);
}

it("offers a discard action for an unreadable transcript draft record", async () => {
	const discard = vi.fn();
	const tree = editor(unreadable, discard);
	const action = tree.root
		.findAllByProps({ accessibilityRole: "button" })
		.find((node) => node.props.accessibilityLabel === "Discard unreadable draft");
	expect(action).toBeDefined();
	// Enabled: a connected screen with an unreadable record must be able to
	// press the one recovery the store permits.
	expect(action?.props.accessibilityState).toMatchObject({ disabled: false });
	await act(async () => action?.props.onPress());
	expect(discard).toHaveBeenCalledTimes(1);
});

it("does not offer the unreadable-record discard when the record is readable or absent", () => {
	const tree = editor(
		{ ...unreadable, draftUnreadable: false, storageUnavailable: false, error: null },
		() => {},
	);
	expect(
		tree.root
			.findAllByProps({ accessibilityRole: "button" })
			.some(
				(node) => node.props.accessibilityLabel === "Discard unreadable draft",
			),
	).toBe(false);
});

it("disables the unreadable-record discard while disconnected", () => {
	// The transcript recovery is live-model-only: unlike keybindings there is no
	// store-free offline discard, so the action must not read as available when
	// no model is reachable.
	const tree = render(
		<TranscriptPreferencesEditor
			hubName="Work hub"
			state={unreadable}
			connected={false}
			edit={() => {}}
			save={() => {}}
			refresh={() => {}}
			discard={() => {}}
			rebase={() => {}}
		/>,
	);
	const action = tree.root
		.findAllByProps({ accessibilityRole: "button" })
		.find((node) => node.props.accessibilityLabel === "Discard unreadable draft");
	expect(action?.props.accessibilityState).toMatchObject({ disabled: true });
});

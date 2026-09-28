// The session sheet's own runtime controls must agree with the nav bar's
// "Shut down" gate (sessionState.ts's SHUT_DOWN set): a session whose state
// line already reads "Shut down" is not one this sheet offers to stop again,
// even when the hub's uncalibrated-capability fallback still reports
// `capabilities.shutdown: true` (#2683). Stopping the runtime narrates nothing
// through the shared controls (ruling 19), so the sheet shows its own
// "Session shut down" confirmation, as the ⋯ menu's toast does.
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { alertRequests, render, renderedText } from "./renderNative.testkit";
import { SessionSheet } from "./SessionSheet";
import type { MobileConversation } from "./projectedRows";
import type { SessionControls } from "./sessionControls";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

function conversation(status: string): MobileConversation {
	return {
		ref: "local:s",
		name: "Session",
		status: { type: status },
		goal: null,
		resumeRequired: false,
		capabilities: {
			rename: false,
			goal: false,
			compact: false,
			shutdown: true,
		},
	} as unknown as MobileConversation;
}

/** A SessionControls whose one observable behavior is the shutdown it is
 * asked to run. getSnapshot returns a stable reference, as
 * useSyncExternalStore requires. */
function fakeControls(shutdown: () => Promise<boolean> = async () => true) {
	const state = {
		pending: null,
		lastAction: null,
		error: null,
		notice: null,
		catalog: null,
		loadingModels: false,
		modelError: null,
	};
	const shutdownSpy = vi.fn(shutdown);
	return {
		shutdown: shutdownSpy,
		subscribe: () => () => {},
		getSnapshot: () => state,
		rename: vi.fn(async () => {}),
		compact: vi.fn(async () => {}),
		forceStop: vi.fn(async () => {}),
		resume: vi.fn(async () => {}),
	} as unknown as SessionControls & { shutdown: typeof shutdownSpy };
}

function sheet(conversationValue: MobileConversation, controls: SessionControls) {
	return (
		<SessionSheet
			conversation={conversationValue}
			hubName="Work hub"
			controls={controls}
			ready
			close={() => {}}
			editGoal={() => {}}
			clearGoal={() => {}}
			goalDisabled={false}
			goalError={null}
		/>
	);
}

/** The pressable the sheet renders for `label`. */
function action(tree: ReturnType<typeof render>, label: string) {
	return tree.root
		.findAll((node) => node.props.accessibilityRole === "button")
		.find((node) => node.props.accessibilityLabel === label);
}

beforeEach(() => {
	alertRequests.length = 0;
});

it("offers no Stop runtime on an already shut-down session, even if the hub still reports the capability", () => {
	// The hub's fallback (appsource's local_daemon.go) reports every capability
	// as true before it has probed them, so `capabilities.shutdown` can be true
	// on a session whose own status is already notLoaded/closed/ended.
	for (const status of ["notLoaded", "closed", "ended"] as const) {
		const tree = render(sheet(conversation(status), fakeControls()));
		expect(action(tree, "Stop runtime")).toBeUndefined();
		tree.unmount();
	}
});

it("still offers Stop runtime on a session that is not shut down", () => {
	const tree = render(sheet(conversation("idle"), fakeControls()));
	expect(action(tree, "Stop runtime")).toBeDefined();
	tree.unmount();
});

it("shows its own 'Session shut down' confirmation after stopping the runtime", async () => {
	const controls = fakeControls();
	const tree = render(sheet(conversation("idle"), controls));

	act(() => action(tree, "Stop runtime")?.props.onPress());
	const [confirm] = alertRequests;
	expect(confirm?.title).toBe("Stop this runtime?");
	await act(async () => {
		confirm?.buttons?.[1]?.onPress?.();
	});

	expect(controls.shutdown).toHaveBeenCalledTimes(1);
	expect(renderedText(tree)).toContain("Session shut down");
	tree.unmount();
});

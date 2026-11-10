// The Session's one Send and the tray's Stop, on the real ConversationScreen:
// what a person presses, and which requests reach the hub through the durable
// runtime. Only native edges are mocked, as in
// ConversationScreen.recovery.test.tsx.
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, vi } from "vitest";
import { markers } from "./markerTestUtils";
import { pressable, settle } from "./renderNative.testkit";

export async function advanceFakeTimers(ms: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
}

export async function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	expect(target.props.accessibilityState).toMatchObject({ disabled: false });
	act(() => target.props.onPress());
	await settle();
}

/** What the session's toasts say happened, by kind. */
export function toasts(tree: ReactTestRenderer): string[] {
	return markers(tree, "toast");
}

/** Why the composer's last action failed, by reason. */
export function actionErrors(tree: ReactTestRenderer): string[] {
	return markers(tree, "action-error");
}

/** Every ghost on the screen, by state. */
export function ghostStates(tree: ReactTestRenderer): string[] {
	return markers(tree, "ghost");
}

/** Whether the screen says a steer failed: its toast, or a refused ghost. */
export function saysSteerFailed(tree: ReactTestRenderer): boolean {
	return toasts(tree).some((kind) => kind.startsWith("steer:")) || ghostStates(tree).includes("refused");
}

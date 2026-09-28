import { describe, expect, it, vi } from "vitest";
import { pressable, render, renderedText } from "../renderNative.testkit";
import { SessionNotice } from "./SessionNotice";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

describe("the notice in the composer's place (ruling 20)", () => {
	it("asks for a restart when the session runs an older Evener", () => {
		const onPress = vi.fn();
		const tree = render(<SessionNotice kind="restartNeeded" busy={false} onPress={onPress} />);
		expect(renderedText(tree)).toContain("This session runs an older Evener. Restart it to pick up the hub's update.");
		const button = pressable(tree, "Restart session");
		expect(button?.props.accessibilityState).toMatchObject({ disabled: false });
		button?.props.onPress();
		expect(onPress).toHaveBeenCalledOnce();
	});

	it("offers Resume when the session is paused", () => {
		const onPress = vi.fn();
		const tree = render(<SessionNotice kind="paused" busy={false} onPress={onPress} />);
		expect(renderedText(tree)).toContain("This session is paused.");
		pressable(tree, "Resume")?.props.onPress();
		expect(onPress).toHaveBeenCalledOnce();
	});

	it("says Restarting… and takes no second press while it restarts", () => {
		const tree = render(<SessionNotice kind="restartNeeded" busy onPress={() => {}} />);
		expect(renderedText(tree)).toContain("Restarting…");
		expect(pressable(tree, "Restarting…")?.props.disabled).toBe(true);
	});

	it("takes no press while it resumes, or while nothing could act on it", () => {
		expect(pressable(render(<SessionNotice kind="paused" busy onPress={() => {}} />), "Resume")?.props.disabled).toBe(
			true,
		);
		expect(
			pressable(render(<SessionNotice kind="paused" busy={false} disabled onPress={() => {}} />), "Resume")?.props
				.disabled,
		).toBe(true);
	});
});

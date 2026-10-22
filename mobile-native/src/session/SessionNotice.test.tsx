import { describe, expect, it, vi } from "vitest";
import { markers } from "../markerTestUtils";
import { pressable, render } from "../renderNative.testkit";
import { paletteFor } from "../design/tokens";
import { SessionNotice } from "./SessionNotice";

const palette = paletteFor("light");

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

describe("the notice in the composer's place (ruling 20)", () => {
	it("asks for a restart when the session runs an older Evener", () => {
		const onPress = vi.fn();
		const tree = render(<SessionNotice kind="restartNeeded" busy={false} onPress={onPress} />);
		expect(markers(tree, "session-notice")).toEqual(["restartNeeded"]);
		const button = pressable(tree, "Restart session");
		expect(button?.props.accessibilityState).toMatchObject({ disabled: false });
		button?.props.onPress();
		expect(onPress).toHaveBeenCalledOnce();
	});

	it("says Restarting… and takes no second press while it restarts", () => {
		const tree = render(<SessionNotice kind="restartNeeded" busy onPress={() => {}} />);
		const [button] = tree.root.findAll((node) => String(node.type) === "Pressable");
		expect(button?.props.accessibilityState).toEqual({ disabled: true, busy: true });
		expect(button?.props.disabled).toBe(true);
	});

	it("takes no press while nothing could act on it", () => {
		expect(
			pressable(
				render(<SessionNotice kind="restartNeeded" busy={false} disabled onPress={() => {}} />),
				"Restart session",
			)?.props.disabled,
		).toBe(true);
	});

	it("says what went wrong in one line, and keeps its button as the way to try again", () => {
		const tree = render(
			<SessionNotice kind="restartNeeded" busy={false} error="Couldn't restart this session." onPress={() => {}} />,
		);
		const line = tree.root.find((node) => node.props.accessibilityRole === "alert");
		expect(line.props.children).toBe("Couldn't restart this session.");
		expect(line.props.style).toMatchObject({ color: palette.dangerInk });
		expect(pressable(tree, "Restart session")?.props.disabled).toBe(false);
	});
});

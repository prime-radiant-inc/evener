// The shut-down question: one dialog for every place that asks it.
import { Alert } from "react-native";
import { beforeEach, expect, it, vi } from "vitest";
import { playedHaptics } from "../renderNative.testkit";
import { confirmShutDown } from "./confirmShutDown";

vi.mock("react-native", () => ({
	Alert: { alert: vi.fn() },
	Platform: { OS: "ios" },
	Vibration: { vibrate: vi.fn() },
}));

interface Button {
	text?: string;
	style?: string;
	onPress?(): void;
}
const lastAsk = () => {
	const [title, message, buttons] = vi.mocked(Alert.alert).mock.calls.at(-1) as unknown as [string, string, Button[]];
	return { title, message, buttons };
};

beforeEach(() => {
	vi.mocked(Alert.alert).mockClear();
	playedHaptics.length = 0;
});

it("names the session when the caller has its title, and not otherwise", () => {
	confirmShutDown(() => {}, "Refactor parser");
	const named = lastAsk();
	confirmShutDown(() => {});
	const unnamed = lastAsk();
	expect(named.title).toContain("Refactor parser");
	expect(unnamed.title).not.toContain("Refactor parser");
	expect(unnamed.title).not.toBe(named.title);
	expect(unnamed.message).toBe(named.message);
});

it("shuts down only when the destructive button is pressed", () => {
	const shutDown = vi.fn();
	confirmShutDown(shutDown, "Refactor parser");
	const { buttons } = lastAsk();
	expect(buttons.map((button) => button.style)).toEqual(["cancel", "destructive"]);
	buttons[0].onPress?.();
	expect(shutDown).not.toHaveBeenCalled();
	buttons[1].onPress?.();
	expect(shutDown).toHaveBeenCalledTimes(1);
	expect(playedHaptics).toEqual(["impact:rigid"]);
});

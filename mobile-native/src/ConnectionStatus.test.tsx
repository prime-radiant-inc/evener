import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "./connectionRecovery";
import { render, renderedText, screenConnection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

import { ConnectionStatus } from "./ConnectionStatus";

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

it("says what the rest of the app says, on the shared clock (spec 14)", () => {
	harness.connection = { ...screenConnection(null, "reconnecting"), error: null };
	const tree = render(<ConnectionStatus />);
	// A blip shorter than 2 seconds reconnects without a word.
	expect(tree.toJSON()).toBeNull();
	act(() => {
		vi.advanceTimersByTime(2_000);
	});
	expect(renderedText(tree)).toBe("Work hub · Reconnecting…");
	act(() => {
		vi.advanceTimersByTime(28_000);
	});
	expect(renderedText(tree)).toBe("Work hub · Offline · updated 1m ago");
	expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
});

it("says Update needed at once for a close no retry can fix, with the reason", () => {
	harness.connection = { ...screenConnection(null, "closed"), fatal: true, error: INCOMPATIBLE_VERSIONS };
	const text = renderedText(render(<ConnectionStatus />));
	expect(text).toContain("Work hub · Update needed");
	expect(text).toContain(INCOMPATIBLE_VERSIONS);
});

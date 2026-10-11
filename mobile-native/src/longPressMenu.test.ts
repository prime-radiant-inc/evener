// The touch-and-hold menu's Android fallback (spec 10.2, ruling 25): the
// block's words preview the alert under its own title, and leaving the menu
// reports the close once, whichever way it goes away.
import { Alert } from "react-native";
import { beforeEach, expect, it, vi } from "vitest";
import { menuPreview, showMenu } from "./longPressMenu";

vi.mock("react-native", () => ({
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
	Alert: { alert: vi.fn(), prompt: vi.fn() },
	Platform: { OS: "android" },
}));

interface Button {
	text?: string;
	onPress?(): void;
}
interface Options {
	cancelable?: boolean;
	onDismiss?(): void;
}
type Call = [string, string | undefined, Button[] | undefined, Options | undefined];

const lastCall = () => (vi.mocked(Alert.alert).mock.calls as unknown as Call[]).at(-1);

beforeEach(() => {
	vi.mocked(Alert.alert).mockClear();
});

it("previews a text's first 220 characters on one line", () => {
	expect(menuPreview(`${"a".repeat(210)}\n${"b".repeat(40)}`)).toHaveLength(220);
	expect(menuPreview("  Ship   it  ")).toBe("Ship it");
});

it("shows the preview as the alert's title, with no placeholder heading", () => {
	showMenu([{ name: "copy", label: "Copy", run: () => {} }], "Ship it now");
	const [title, message] = lastCall() ?? [];
	expect(title).toBe("Ship it now");
	expect(message).toBeUndefined();
});

it("reports the close once, before the chosen item runs", () => {
	const order: string[] = [];
	showMenu([{ name: "copy", label: "Copy", run: () => void order.push("run") }], "words", () => order.push("close"));
	const [, , buttons, options] = lastCall() ?? [];
	buttons?.[0]?.onPress?.();
	// A tap on the device dismisses the alert after the choice, reporting again.
	options?.onDismiss?.();
	expect(order).toEqual(["close", "run"]);
});

it("reports the close once when a tap outside dismisses the alert", () => {
	let closes = 0;
	showMenu([{ name: "copy", label: "Copy", run: () => {} }], "words", () => {
		closes += 1;
	});
	const [, , , options] = lastCall() ?? [];
	options?.onDismiss?.();
	options?.onDismiss?.();
	expect(closes).toBe(1);
});

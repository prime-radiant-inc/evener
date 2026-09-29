import { expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { render, renderedText } from "../renderNative.testkit";
import { Connecting, SheetStatus } from "./SheetStatus";

const status = { line: null as string | null, state: "ready", fatal: false };
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: status.state, fatal: status.fatal }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("shows nothing while live, and the line with no button while not", () => {
	status.state = "ready";
	status.line = null;
	expect(render(<SheetStatus />).toJSON()).toBeNull();
	status.state = "reconnecting";
	status.line = "Reconnecting…";
	const tree = render(<SheetStatus />);
	expect(renderedText(tree)).toBe("Reconnecting…");
	expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
});

it("says a never-loaded page is connecting, or why it can't", () => {
	status.fatal = false;
	expect(renderedText(render(<Connecting hubName="magic-kingdom" />))).toBe("Connecting to magic-kingdom…");
	status.fatal = true;
	expect(renderedText(render(<Connecting hubName="magic-kingdom" />))).toBe(INCOMPATIBLE_VERSIONS);
});

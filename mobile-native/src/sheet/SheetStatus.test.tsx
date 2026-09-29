import { expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { render, renderedText } from "../renderNative.testkit";
import { FirstLoad, Loading, SheetStatus } from "./SheetStatus";

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

it("says a never-loaded page is connecting only while it is, or why it can't connect", () => {
	status.fatal = false;
	status.state = "connecting";
	expect(renderedText(render(<FirstLoad hubName="magic-kingdom" />))).toBe("Connecting to magic-kingdom…");
	status.state = "reconnecting";
	expect(renderedText(render(<FirstLoad hubName="magic-kingdom" />))).toBe("Connecting to magic-kingdom…");
	status.fatal = true;
	expect(renderedText(render(<FirstLoad hubName="magic-kingdom" />))).toBe(INCOMPATIBLE_VERSIONS);
	status.fatal = false;
});

it("says nothing about the connection while it's live and the first read is on its way (spec 14)", () => {
	status.state = "ready";
	const tree = render(<FirstLoad hubName="magic-kingdom" />);
	expect(renderedText(tree)).toBe("");
	const spinner = tree.root.findByType("ActivityIndicator" as never);
	expect(spinner.props.accessibilityLabel).toBe("Loading");
});

it("draws every loading spinner in the same padded space", () => {
	const spinner = render(<Loading label="Loading marketplaces" />).root.findByType("ActivityIndicator" as never);
	expect(spinner.props.accessibilityLabel).toBe("Loading marketplaces");
	expect(spinner.props.style).toEqual({ padding: 32 });
});

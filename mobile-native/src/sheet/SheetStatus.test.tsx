import { expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { uiType } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import { FirstLoad, SheetStatus } from "./SheetStatus";

const status = { line: null as string | null, state: "ready", fatal: false };
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: status.state, fatal: status.fatal }) }));
const screen = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: screen.fontScale, scale: 2, width: 390, height: 844 }),
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

// The sheet's status line and a first load's sentence set in spec 16.2's
// roles: Footnote and Subheadline (settings audit Lev5).
it("sets its line in the Footnote role and a first load's sentence in Subheadline", () => {
	status.state = "reconnecting";
	status.line = "Reconnecting…";
	const line = render(<SheetStatus />).root.findByType("Text" as never);
	expect(line.props.style).toMatchObject(uiType.footnote);
	status.line = null;
	status.fatal = false;
	status.state = "connecting";
	const first = render(<FirstLoad hubName="magic-kingdom" label="Loading hosts" />).root.findByType("Text" as never);
	expect(first.props.style).toMatchObject(uiType.subheadline);
});

// Dynamic Type grows both roles, line height with the size.
it("scales both roles with the text size", () => {
	screen.fontScale = 1.5;
	status.state = "reconnecting";
	status.line = "Reconnecting…";
	const line = render(<SheetStatus />).root.findByType("Text" as never);
	expect(line.props.style).toMatchObject({ fontSize: 19.5, lineHeight: 27 });
	status.line = null;
	status.state = "connecting";
	const first = render(<FirstLoad hubName="magic-kingdom" label="Loading hosts" />).root.findByType("Text" as never);
	expect(first.props.style).toMatchObject({ fontSize: 22.5, lineHeight: 30 });
	screen.fontScale = 1;
});

it.each(["connecting", "reconnecting"])("says a never-loaded page is connecting while %s", (state) => {
	status.fatal = false;
	status.state = state;
	expect(renderedText(render(<FirstLoad hubName="magic-kingdom" label="Loading hosts" />))).toBe(
		"Connecting to magic-kingdom…",
	);
});

it.each(["ready", "idle", "closed"])(
	"waits quietly while %s, and names what it waits on for VoiceOver (spec 14)",
	(state) => {
		status.fatal = false;
		status.state = state;
		const tree = render(<FirstLoad hubName="magic-kingdom" label="Loading hosts" />);
		expect(renderedText(tree)).toBe("");
		expect(tree.root.findByType("ActivityIndicator" as never).props.accessibilityLabel).toBe("Loading hosts");
	},
);

it("says why a never-loaded page can't connect, whatever the state", () => {
	status.fatal = true;
	for (const state of ["ready", "connecting", "closed"]) {
		status.state = state;
		expect(renderedText(render(<FirstLoad hubName="magic-kingdom" label="Loading hosts" />))).toBe(
			INCOMPATIBLE_VERSIONS,
		);
	}
	status.fatal = false;
});

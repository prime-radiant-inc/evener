import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import type { BoardState, ClassifiedRow } from "./attention";
import { BoardRow, type BoardRowProps } from "./BoardRow";
import { PulseMeter } from "./PulseMeter";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");
const NOW = Date.UTC(2026, 8, 26, 12, 0);
const minutesAgo = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();

const row = (over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref: "local:fix",
	host_id: "local",
	session_id: "fix",
	title: "Fix Endless Provider Retry Loop",
	project: "evener",
	state: "errored",
	kind: "session",
	live: true,
	children: [],
	updated_at: minutesAgo(2),
	...over,
});
const item = (state: BoardState, over: Partial<NavigationSessionSummary> = {}): ClassifiedRow => ({
	row: row(over),
	state,
});

function mount(over: Partial<BoardRowProps> = {}): ReactTestRenderer {
	const props: BoardRowProps = {
		item: item("failed"),
		variant: "signal",
		moving: false,
		connected: true,
		usual: { project: "evener", host: "local" },
		hostLabel: (hostId) => (hostId === "studio" ? "Studio Mac" : hostId),
		hasDraft: false,
		now: NOW,
		onOpen: () => {},
		...over,
	};
	return render(<BoardRow {...props} />);
}

type Style = Record<string, unknown>;
const flatten = (style: unknown): Style =>
	[style].flat(Number.POSITIVE_INFINITY).reduce<Style>((all, part) => ({ ...all, ...(part ?? {}) }), {});
const styleOf = (node: ReactTestInstance): Style => flatten(node.props.style);
/** The Text whose own children include `content`. */
const textWith = (tree: ReactTestRenderer, content: string) =>
	tree.root.findAll(
		(node) => node.type === ("Text" as never) && [node.props.children].flat().includes(content),
	);
const symbols = (tree: ReactTestRenderer) =>
	tree.root.findAllByType("SymbolView" as never).map((node) => node.props.name as string);
const pressable = (tree: ReactTestRenderer) => tree.root.findByType("Pressable" as never);

describe("a Board row (spec 7.2)", () => {
	it("shows a Failed row's title, the word in danger ink, the reason in ink, and its age", () => {
		const tree = mount();
		const title = textWith(tree, "Fix Endless Provider Retry Loop")[0];
		expect(styleOf(title)).toMatchObject({ fontSize: 17, lineHeight: 22, fontWeight: "600", color: palette.inkHi });
		const word = textWith(tree, "Failed")[0];
		expect(styleOf(word)).toMatchObject({ fontWeight: "600", color: palette.dangerInk });
		const reason = textWith(tree, "open the session to see what went wrong")[0];
		expect(styleOf(reason)).toMatchObject({ fontSize: 15, lineHeight: 20, color: palette.inkHi });
		const age = textWith(tree, "2m")[0];
		expect(styleOf(age)).toMatchObject({ fontSize: 13, color: palette.inkLow, fontVariant: ["tabular-nums"] });
	});

	it("colors an attention word in the attention ink", () => {
		const tree = mount({ item: item("question", { state: "awaiting", ask_pending: true }) });
		expect(styleOf(textWith(tree, "Question")[0])).toMatchObject({ color: palette.attentionInk });
	});

	it("hides the age when the row has no time", () => {
		const tree = mount({ item: item("failed", { updated_at: undefined }) });
		expect(textWith(tree, "Fix Endless Provider Retry Loop")).toHaveLength(1);
		expect(tree.root.findAll((node) => node.props.style && styleOf(node).fontVariant !== undefined)).toEqual([]);
	});

	it("gives a Needs you title two lines and a Working title one, both tail-truncated", () => {
		const needsYou = textWith(mount(), "Fix Endless Provider Retry Loop")[0];
		expect(needsYou.props).toMatchObject({ numberOfLines: 2, ellipsizeMode: "tail" });
		const working = textWith(
			mount({ item: item("working", { state: "active" }), moving: true }),
			"Fix Endless Provider Retry Loop",
		)[0];
		expect(working.props).toMatchObject({ numberOfLines: 1, ellipsizeMode: "tail" });
	});

	it("lets a Needs you why line run to two lines and a Working one to one", () => {
		const needsYou = textWith(mount(), "open the session to see what went wrong")[0];
		expect(needsYou.props.numberOfLines).toBe(2);
		const tree = mount({
			item: item("working", { state: "active", running_jobs: [{ command: "go test ./agent/..." } as never] }),
		});
		const activity = textWith(tree, "Running go test ./agent/...")[0];
		expect(activity.props.numberOfLines).toBe(1);
		expect(styleOf(activity)).toMatchObject({ color: palette.inkMid });
		expect(textWith(tree, "Working")).toEqual([]);
	});

	it("keeps a quiet row to the mark, one line of title and the age, 48pt tall", () => {
		const tree = mount({ variant: "quiet", item: item("failed", { project: "magic-kingdom", host_id: "studio" }) });
		expect(textWith(tree, "Fix Endless Provider Retry Loop")[0].props.numberOfLines).toBe(1);
		expect(textWith(tree, "2m")).toHaveLength(1);
		expect(textWith(tree, "Failed")).toEqual([]);
		expect(textWith(tree, "open the session to see what went wrong")).toEqual([]);
		expect(textWith(tree, "magic-kingdom")).toEqual([]);
		expect(symbols(tree)).not.toContain("folder");
		expect(pressedStyle(tree, false).minHeight).toBe(48);
		expect(pressable(tree).props.accessibilityLabel).toBe("Fix Endless Provider Retry Loop, Failed, 2 minutes");
	});

	it("shows the Draft tag only when the session has a draft", () => {
		expect(textWith(mount(), "Draft")).toEqual([]);
		const tag = textWith(mount({ hasDraft: true }), "Draft")[0];
		expect(styleOf(tag)).toMatchObject({ fontSize: 11, lineHeight: 13, fontWeight: "600", color: palette.accentInk });
	});

	it("prints only the unusual project and host on the last line", () => {
		const usual = mount();
		expect(symbols(usual)).not.toContain("folder");
		expect(symbols(usual)).not.toContain("server.rack");

		const project = mount({ item: item("failed", { project: "magic-kingdom" }) });
		expect(textWith(project, "magic-kingdom")).toHaveLength(1);
		expect(symbols(project)).toContain("folder");
		expect(symbols(project)).not.toContain("server.rack");

		const host = mount({ item: item("failed", { host_id: "studio" }) });
		const label = textWith(host, "Studio Mac")[0];
		expect(label.props.numberOfLines).toBe(1);
		expect(styleOf(label)).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkLow });
		expect(symbols(host)).toContain("server.rack");
		expect(symbols(host)).not.toContain("folder");
		const glyph = host.root.findAll((node) => node.props.name === "server.rack")[0];
		expect(glyph.props).toMatchObject({ size: 13, tintColor: palette.inkLow });
	});

	it("shows task progress on the last line with a checklist glyph", () => {
		const tree = mount({
			item: item("failed", {
				tasks: { total: 7, done: 3, current: "Fix the settle/drain race" },
			}),
		});
		const line = textWith(tree, "Task 4 of 7 · Fix the settle/drain race")[0];
		expect(line.props).toMatchObject({ numberOfLines: 1, ellipsizeMode: "tail" });
		expect(styleOf(line)).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkLow });
		expect(symbols(tree)).toContain("checklist");
	});

	it("shows no task progress once the list is finished or when there is none", () => {
		const finished = mount({ item: item("failed", { tasks: { total: 2, done: 2 } }) });
		expect(symbols(finished)).not.toContain("checklist");
		const none = mount();
		expect(symbols(none)).not.toContain("checklist");
	});

	it("puts task progress ahead of project and host on the last line", () => {
		const tree = mount({
			item: item("failed", {
				project: "magic-kingdom",
				host_id: "studio",
				tasks: { total: 7, done: 3, current: "Fix the settle/drain race" },
			}),
		});
		const glyphs = symbols(tree);
		expect(glyphs.indexOf("checklist")).toBeGreaterThanOrEqual(0);
		expect(glyphs.indexOf("checklist")).toBeLessThan(glyphs.indexOf("folder"));
		expect(glyphs.indexOf("folder")).toBeLessThan(glyphs.indexOf("server.rack"));
	});

	it("moves a working row in Live with the pulse meter, and shows a still dot elsewhere", () => {
		const live = mount({ item: item("working", { state: "active" }), moving: true });
		expect(live.root.findAllByType(PulseMeter)).toHaveLength(1);
		const pinned = mount({ item: item("working", { state: "active" }), moving: false });
		expect(pinned.root.findAllByType(PulseMeter)).toEqual([]);
		expect(symbols(pinned)).toContain("circle.fill");
	});

	it("grays the meter while disconnected", () => {
		const tree = mount({ item: item("working", { state: "active" }), moving: true, connected: false });
		expect(tree.root.findByType(PulseMeter).props.tone).toBe("gray");
	});

	it("reads one label in order: title, state, reason, age", () => {
		expect(pressable(mount()).props.accessibilityLabel).toBe(
			"Fix Endless Provider Retry Loop, Failed, open the session to see what went wrong, 2 minutes",
		);
		const working = mount({
			item: item("working", {
				state: "active",
				updated_at: minutesAgo(60),
				children: [row({ ref: "local:child", state: "active" })],
			}),
		});
		expect(pressable(working).props.accessibilityLabel).toBe(
			"Fix Endless Provider Retry Loop, Working, Waiting on 1 subagent, 1 hour",
		);
		const idle = mount({ variant: "quiet", item: item("idle", { state: "idle", updated_at: minutesAgo(3 * 1440) }) });
		expect(pressable(idle).props.accessibilityLabel).toBe("Fix Endless Provider Retry Loop, Idle, 3 days");
		const fresh = mount({ variant: "quiet", item: item("idle", { state: "idle", updated_at: minutesAgo(0) }) });
		expect(pressable(fresh).props.accessibilityLabel).toBe("Fix Endless Provider Retry Loop, Idle, now");
		const timeless = mount({ variant: "quiet", item: item("idle", { state: "idle", updated_at: undefined }) });
		expect(pressable(timeless).props.accessibilityLabel).toBe("Fix Endless Provider Retry Loop, Idle");
	});

	it("is one button that opens the session, darkening while pressed", () => {
		const opened: NavigationSessionSummary[] = [];
		const current = item("failed");
		const tree = mount({ item: current, onOpen: (session) => opened.push(session) });
		const button = pressable(tree);
		expect(button.props.accessibilityRole).toBe("button");
		button.props.onPress();
		expect(opened).toEqual([current.row]);
		expect(pressedStyle(tree, true).backgroundColor).toBe(palette.pressed);
		expect(pressedStyle(tree, false).backgroundColor).not.toBe(palette.pressed);
		expect(pressedStyle(tree, false)).toMatchObject({ paddingHorizontal: 16 });
		expect(pressedStyle(tree, false).minHeight).toBeGreaterThanOrEqual(44);
	});
});

/** The Pressable's style for a press state: the inert test host never calls
 * its style function, so the test does. */
function pressedStyle(tree: ReactTestRenderer, pressed: boolean): Style {
	const style = pressable(tree).props.style;
	return typeof style === "function" ? flatten(style({ pressed })) : {};
}

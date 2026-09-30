import type { NavigationSessionSummary } from "@evener/appwire-client";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { DisplayProvider } from "../display/displayContext";
import { DisplayPreferences } from "../display/displayPreferences";
import { render } from "../renderNative.testkit";
import type { BoardState, ClassifiedRow } from "./attention";
import { BoardRow, type BoardRowProps, sessionSubagentChip } from "./BoardRow";
import { PulseMeter } from "./PulseMeter";
import { StateMark } from "./StateMark";

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
		msSinceRead: null,
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
	tree.root.findAll((node) => node.type === ("Text" as never) && [node.props.children].flat().includes(content));
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
		expect(styleOf(label)).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkLow, flexShrink: 0 });
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
		expect(styleOf(line)).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkLow, flexShrink: 1 });
		expect(symbols(tree)).toContain("checklist");
	});

	it("never shrinks project or host, so a long task title is what gives way under pressure", () => {
		const tree = mount({
			item: item("failed", {
				project: "magic-kingdom",
				host_id: "studio",
				tasks: { total: 7, done: 3, current: "Fix the settle/drain race" },
			}),
		});
		expect(styleOf(textWith(tree, "magic-kingdom")[0])).toMatchObject({ flexShrink: 0 });
		expect(styleOf(textWith(tree, "Studio Mac")[0])).toMatchObject({ flexShrink: 0 });
		expect(styleOf(textWith(tree, "Task 4 of 7 · Fix the settle/drain race")[0])).toMatchObject({ flexShrink: 1 });
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
		expect(symbols(tree).slice(-3)).toEqual(["checklist", "folder", "server.rack"]);
	});

	// "Show model on Board rows" (spec 7.2, 12): the model's name ends the
	// last line, in the line's ink, only while the setting is on.
	it("ends the last line with the model's name when Show model on Board rows is on", () => {
		const stored = new Map<string, string>();
		const prefs = new DisplayPreferences({
			getItemSync: (key) => stored.get(key) ?? null,
			setItemSync: (key, value) => void stored.set(key, value),
		});
		const props: BoardRowProps = {
			item: item("failed", { host_id: "studio", model_name: "GLM 5.3 Vision" }),
			variant: "signal",
			moving: false,
			connected: true,
			usual: { project: "evener", host: "local" },
			hostLabel: (hostId) => hostId,
			hasDraft: false,
			msSinceRead: null,
			now: NOW,
			onOpen: () => {},
		};
		const tree = render(
			<DisplayProvider value={prefs}>
				<BoardRow {...props} />
			</DisplayProvider>,
		);
		expect(textWith(tree, "GLM 5.3 Vision")).toHaveLength(0);
		act(() => prefs.set({ showModel: true }));
		const model = textWith(tree, "GLM 5.3 Vision")[0];
		expect(styleOf(model)).toMatchObject({ fontSize: 13, color: palette.inkLow });
		// Last on the line: after the host.
		const lastLine = tree.root.findAll((node) => node.type === ("Text" as never) && node.props.numberOfLines === 1);
		expect(lastLine.map((node) => node.props.children).slice(-2)).toEqual(["studio", "GLM 5.3 Vision"]);
	});

	it("moves a working row in Live with the pulse meter, and shows a still dot elsewhere", () => {
		const live = mount({ item: item("working", { state: "active" }), moving: true });
		expect(live.root.findAllByType(PulseMeter)).toHaveLength(1);
		const pinned = mount({ item: item("working", { state: "active" }), moving: false });
		expect(pinned.root.findAllByType(PulseMeter)).toEqual([]);
		expect(symbols(pinned)).toContain("circle.fill");
	});

	it("draws a working row's meter from its activity read, and flat without one", () => {
		const minutes = [0, 0, 1, 4, 9, 2, 5];
		const read = mount({
			item: item("working", { state: "active" }),
			moving: true,
			activity: { ref: "local:fix", minutes, runningSubagents: 0 },
		});
		expect(read.root.findByType(PulseMeter).props.perMinute).toEqual(minutes);
		const unread = mount({ item: item("working", { state: "active" }), moving: true });
		expect(unread.root.findByType(PulseMeter).props.perMinute).toBeUndefined();
	});

	it("says what a working row's activity read says in place of the row's own tally", () => {
		const busy = item("working", {
			state: "active",
			subagents: { running: 1, failed: 0, done: 0 },
		});
		const guessed = mount({ item: busy });
		expect(textWith(guessed, "Waiting on 1 subagent")).toHaveLength(1);
		const read = mount({ item: busy, activity: { ref: "local:fix", minutes: [1], runningSubagents: 3 } });
		expect(textWith(read, "Waiting on 3 subagents")).toHaveLength(1);
		expect(textWith(read, "Waiting on 1 subagent")).toEqual([]);
	});

	it("chips a live root's subagent tally, with a failure in the danger ink", () => {
		const running = mount({
			item: item("working", { state: "active", subagents: { running: 2, failed: 0, done: 4 } }),
		});
		expect(textWith(running, "2 running")).toHaveLength(1);
		const chip = running.root.findAll((node) => node.props.testID === "subagent-chip");
		expect(chip).toHaveLength(1);
		// The row is one accessibility element; it speaks the tally once. The why
		// line already names the running count, so the chip adds nothing here.
		expect(pressable(running).props.accessibilityLabel).toContain("Waiting on 2 subagents");
		expect(pressable(running).props.accessibilityLabel).not.toContain("2 running");
		// A quiet row has no why line to name the count, so the chip speaks it.
		const quiet = mount({
			variant: "quiet",
			item: item("working", { state: "active", subagents: { running: 2, failed: 0, done: 0 } }),
		});
		expect(pressable(quiet).props.accessibilityLabel).toContain("2 running");

		const failed = mount({ item: item("failed", { subagents: { running: 0, failed: 3, done: 4 } }) });
		const chipText = textWith(failed, "3 failed")[0];
		expect(styleOf(chipText)).toMatchObject({ color: palette.dangerInk });
		// No why line names subagents on a failed row, so the row label carries
		// the chip's count itself.
		expect(pressable(failed).props.accessibilityLabel).toContain("3 failed");

		// A mixed tally keeps the running run neutral and colors only the failure.
		const mixed = mount({
			item: item("working", { state: "active", subagents: { running: 2, failed: 3, done: 1 } }),
		});
		expect(styleOf(textWith(mixed, "2 running")[0])).toMatchObject({ color: palette.inkMid });
		expect(styleOf(textWith(mixed, "3 failed")[0])).toMatchObject({ color: palette.dangerInk });

		// A done-only tally is history (as the web rail's chip reads it), and a
		// past row carries no tally at all (D6): neither shows a chip.
		for (const settled of [
			item("working", { state: "active", subagents: { running: 0, failed: 0, done: 5 } }),
			item("working", { state: "active", live: false, subagents: { running: 2, failed: 0, done: 0 } }),
			item("working", { state: "active", kind: "fork", subagents: { running: 2, failed: 0, done: 0 } }),
		]) {
			expect(mount({ item: settled }).root.findAll((node) => node.props.testID === "subagent-chip")).toEqual([]);
		}
	});

	it("the shared chip renderer answers null when a row has nothing to show", () => {
		// The lists gate their title row on this, so a row with no tally (or a
		// done-only history) must not look like it has a chip.
		expect(sessionSubagentChip(row())).toBeNull();
		expect(sessionSubagentChip(row({ subagents: { running: 0, failed: 0, done: 3 } }))).toBeNull();
		expect(sessionSubagentChip(row({ subagents: { running: 1, failed: 0, done: 0 } }))).not.toBeNull();
		// Only a live root: a nested fork original shows no chip even with a tally,
		// as the web rail's chip gates on isTopLevelSession.
		expect(sessionSubagentChip(row({ kind: "fork", subagents: { running: 1, failed: 0, done: 0 } }))).toBeNull();
	});

	it("counts a working row's quiet time from its read, plus the time since that read landed", () => {
		const tree = mount({
			item: item("working", { state: "active" }),
			activity: { ref: "local:fix", minutes: [0], runningSubagents: 0, quietForMs: 3 * 60_000 },
			msSinceRead: 60_000,
		});
		const quiet = textWith(tree, "Quiet 4m")[0];
		expect(styleOf(quiet)).toMatchObject({ color: palette.inkMid });
	});

	it("colors the whole May be stuck line in the attention ink", () => {
		const tree = mount({
			item: item("working", { state: "active" }),
			activity: { ref: "local:fix", minutes: [0], runningSubagents: 0, quietForMs: 12 * 60_000 },
			msSinceRead: 0,
		});
		const stuck = textWith(tree, "May be stuck · no updates for 12m")[0];
		expect(styleOf(stuck)).toMatchObject({ fontSize: 15, color: palette.attentionInk });
		expect(pressable(tree).props.accessibilityLabel).toContain("May be stuck · no updates for 12m");
	});

	it("grays the meter while disconnected", () => {
		const tree = mount({ item: item("working", { state: "active" }), moving: true, connected: false });
		expect(tree.root.findByType(PulseMeter).props.tone).toBe("gray");
	});

	it("turns a stuck row's meter amber, unless the connection itself is down (spec 13.1, 16.4)", () => {
		const stuckActivity = { ref: "local:fix", minutes: [0], runningSubagents: 0, quietForMs: 12 * 60_000 };
		const stuck = mount({
			item: item("working", { state: "active" }),
			moving: true,
			activity: stuckActivity,
			msSinceRead: 0,
		});
		expect(stuck.root.findByType(PulseMeter).props.tone).toBe("attention");

		const offlineButStuck = mount({
			item: item("working", { state: "active" }),
			moving: true,
			connected: false,
			activity: stuckActivity,
			msSinceRead: 0,
		});
		expect(offlineButStuck.root.findByType(PulseMeter).props.tone).toBe("gray");
	});

	it("reads one label in order: title, state, reason, age", () => {
		expect(pressable(mount()).props.accessibilityLabel).toBe(
			"Fix Endless Provider Retry Loop, Failed, open the session to see what went wrong, 2 minutes",
		);
		const working = mount({
			item: item("working", {
				state: "active",
				updated_at: minutesAgo(60),
				subagents: { running: 1, failed: 0, done: 0 },
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

	it("dims to half opacity and reads busy while its change is on its way", () => {
		const tree = mount({ dimmed: true });
		expect(pressedStyle(tree, false).opacity).toBe(0.5);
		expect(pressable(tree).props.accessibilityState).toEqual({ busy: true });
		const plain = mount();
		expect(pressedStyle(plain, false).opacity).toBe(1);
		expect(pressable(plain).props.accessibilityState).toEqual({ busy: false });
	});

	it("gives VoiceOver the row's actions on its one button", () => {
		const onAccessibilityAction = vi.fn();
		const tree = mount({
			accessibilityActions: [{ name: "archive", label: "Archive" }],
			onAccessibilityAction,
		});
		const button = pressable(tree);
		expect(button.props.accessibilityActions).toEqual([{ name: "archive", label: "Archive" }]);
		button.props.onAccessibilityAction({ nativeEvent: { actionName: "archive" } });
		expect(onAccessibilityAction).toHaveBeenCalledWith({ nativeEvent: { actionName: "archive" } });
	});

	it("shows select mode's checkbox in place of its state mark, and reads selected", () => {
		const marks = (tree: ReactTestRenderer) =>
			tree.root.findAllByType("SymbolView" as never).map((node) => [node.props.name, node.props.tintColor]);
		const chosen = mount({ selected: true });
		expect(marks(chosen)[0]).toEqual(["checkmark.circle.fill", palette.accent]);
		expect(chosen.root.findAllByType(StateMark)).toHaveLength(0);
		expect(pressable(chosen).props.accessibilityRole).toBe("button");
		expect(pressable(chosen).props.accessibilityState).toEqual({ busy: false, selected: true });
		const open = mount({ selected: false });
		expect(marks(open)[0]).toEqual(["circle", palette.inkLow]);
		expect(pressable(open).props.accessibilityState).toEqual({ busy: false, selected: false });
		// Out of select mode it shows its state.
		expect(mount().root.findAllByType(StateMark)).toHaveLength(1);
	});

	it("washes amber behind its content, fading out, when it has just entered Needs you", () => {
		const washes = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.type === ("Animated.View" as never));
		const tree = mount({ wash: 2 });
		const [wash] = washes(tree);
		expect(washes(tree)).toHaveLength(1);
		expect(wash.props.pointerEvents).toBe("none");
		const style = styleOf(wash);
		expect(style).toMatchObject({ position: "absolute", top: 0, right: 0, bottom: 0, left: 0 });
		expect(style.backgroundColor).toBe(palette.attentionBg);
		// The mocked timing lands on its target at once: faded out.
		expect((style.opacity as { value: number }).value).toBe(0);
		// It sits behind the row's content: the button's first child.
		const [first] = pressable(tree).children as ReactTestInstance[];
		expect(washes({ root: first } as ReactTestRenderer)).toEqual([wash]);
		expect(washes(mount({ wash: 0 }))).toHaveLength(0);
		expect(washes(mount())).toHaveLength(0);
	});
});

/** The Pressable's style for a press state: the inert test host never calls
 * its style function, so the test does. */
function pressedStyle(tree: ReactTestRenderer, pressed: boolean): Style {
	const style = pressable(tree).props.style;
	return typeof style === "function" ? flatten(style({ pressed })) : {};
}

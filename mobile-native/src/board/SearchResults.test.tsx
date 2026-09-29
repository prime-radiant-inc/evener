import type { NavigationProjectSummary, SearchResult } from "@evener/appwire-client";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import type { SearchSnapshot } from "./boardSearch";
import { SearchResults, type SearchResultsProps } from "./SearchResults";
import { StateMark } from "./StateMark";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");
const result = (id: string, over: Partial<SearchResult> = {}): SearchResult => ({
	id,
	title: id,
	project: "evener",
	state: "idle",
	age: "2m",
	ref: `local:${id}`,
	...over,
});
const failing = result("fail", { title: "Fix retry loop", state: "errored", age: "3m" });
const asking = result("ask", { title: "Pick a name", state: "awaiting", askPending: true, age: "1h" });
const past = result("past", { title: "Old report", state: "ended", age: "2d", project: "reports" });
const answered: SearchSnapshot = {
	query: "fix",
	results: { live: [failing, asking], past: [past] },
	searching: false,
	failed: false,
};

function mount(over: Partial<SearchResultsProps> = {}) {
	const props: SearchResultsProps = {
		search: answered,
		scope: "all",
		onScope: vi.fn(),
		connected: true,
		recent: [],
		onOpen: vi.fn(),
		onRecent: vi.fn(),
		onClearRecent: vi.fn(),
		projects: [],
		onOpenProject: vi.fn(),
		...over,
	};
	return { tree: render(<SearchResults {...props} />), props };
}
type Style = Record<string, unknown>;
const flatten = (style: unknown): Style =>
	[typeof style === "function" ? style({ pressed: false }) : style]
		.flat(Number.POSITIVE_INFINITY)
		.reduce<Style>((all, part) => ({ ...all, ...(part ?? {}) }), {});
const pressables = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.type === ("Pressable" as never));
const labelled = (tree: ReactTestRenderer, label: string): ReactTestInstance =>
	tree.root.find((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === label);
const textWith = (within: ReactTestRenderer | ReactTestInstance, content: string) =>
	("root" in within ? within.root : within).find(
		(node) => node.type === ("Text" as never) && node.props.children === content,
	);
const resultRows = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.props.testID === "search-result");
const projectRows = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.props.testID === "project-result");
const evener: NavigationProjectSummary = {
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 2,
};
const unnamed: NavigationProjectSummary = { key: "tools", name: "", working_dir: "/srv/tools", session_count: 1 };

it("lists live results then past ones, each with its mark, project and age", () => {
	const { tree } = mount();
	const rows = resultRows(tree);
	expect(rows.map((row) => row.props.accessibilityLabel)).toEqual([
		"Fix retry loop, Failed, evener, 3 minutes",
		"Pick a name, Question, evener, 1 hour",
		// Shut down and idle rows carry no mark, so the label names no state.
		"Old report, reports, 2 days",
	]);
	expect(rows.map((row) => row.findByType(StateMark).props.state)).toEqual(["failed", "question", "shutDown"]);
	expect(textWith(tree, "SESSIONS · 3")).toBeTruthy();
	const age = textWith(rows[0], "3m");
	expect(flatten(age.props.style)).toMatchObject({
		fontSize: 13,
		color: palette.inkLow,
		fontVariant: ["tabular-nums"],
	});
	expect(flatten(rows[0].props.style).minHeight).toBeGreaterThanOrEqual(44);
});

it("lists only live results in the Live scope", () => {
	const { tree } = mount({ scope: "live" });
	expect(resultRows(tree)).toHaveLength(2);
	expect(textWith(tree, "SESSIONS · 2")).toBeTruthy();
});

it("sizes result rows like the Board rows they mirror: a session a signal row, a project a quiet one", () => {
	const sessions = resultRows(mount().tree);
	expect(sessions.map((row) => flatten(row.props.style).minHeight)).toEqual([64, 64, 64]);
	const projects = projectRows(mount({ projects: [evener] }).tree);
	expect(projects.map((row) => flatten(row.props.style).minHeight)).toEqual([48]);
});

it("offers the All and Live scopes, marking the one chosen", () => {
	const { tree, props } = mount({ scope: "live" });
	const all = labelled(tree, "All");
	const live = labelled(tree, "Live");
	expect(all.props.accessibilityState).toEqual({ selected: false });
	expect(live.props.accessibilityState).toEqual({ selected: true });
	expect(flatten(textWith(live, "Live").props.style).color).toBe(palette.accentInk);
	act(() => all.props.onPress());
	expect(props.onScope).toHaveBeenCalledWith("all");
	// Archived waits for S14.
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Archived")).toHaveLength(0);
});

it("opens a result when it's tapped", () => {
	const { tree, props } = mount();
	act(() => resultRows(tree)[1].props.onPress());
	expect(props.onOpen).toHaveBeenCalledWith(asking);
});

it("says so when nothing matches", () => {
	const { tree } = mount({ search: { ...answered, results: { live: [], past: [past] } }, scope: "live" });
	expect(renderedText(tree)).toContain("No sessions match.");
	expect(resultRows(tree)).toHaveLength(0);
});

it("says it's searching while the hub has the query and nothing has landed", () => {
	const { tree } = mount({ search: { query: "fix", results: null, searching: true, failed: false } });
	expect(renderedText(tree)).toContain("Searching…");
	// Nothing to show before the debounce ends.
	const resting = mount({ search: { query: "fix", results: null, searching: false, failed: false } });
	expect(renderedText(resting.tree)).not.toContain("Searching…");
	expect(resultRows(resting.tree)).toHaveLength(0);
});

it("says a failed search failed, in ink, and never asks you to retry", () => {
	const { tree } = mount({ search: { query: "fix", results: null, searching: false, failed: true } });
	const text = renderedText(tree);
	expect(text).toContain("Couldn't search this hub's sessions.");
	expect(flatten(textWith(tree, "Couldn't search this hub's sessions.").props.style).color).toBe(palette.inkMid);
	expect(text).not.toMatch(/retry|try again|refresh/i);
	// Only the scope chips are controls.
	expect(pressables(tree).map((node) => node.props.accessibilityLabel)).toEqual(["All", "Live"]);
});

it("says search waits for the hub while it's out of reach", () => {
	const { tree } = mount({
		connected: false,
		search: { query: "fix", results: null, searching: false, failed: false },
	});
	expect(renderedText(tree)).toContain("Search works when the hub is connected.");
});

it("keeps the last answer's rows while the hub is out of reach", () => {
	const { tree } = mount({ connected: false });
	expect(resultRows(tree)).toHaveLength(3);
	expect(renderedText(tree)).not.toContain("Search works when the hub is connected.");
});

it("shows recent searches when the field is empty, each one searchable, with Clear", () => {
	const empty: SearchSnapshot = { query: "", results: null, searching: false, failed: false };
	const { tree, props } = mount({ search: empty, recent: ["fix", "docs"] });
	expect(textWith(tree, "RECENT")).toBeTruthy();
	expect(resultRows(tree)).toHaveLength(0);
	// The scope chips show with the field, before there's a query (spec 7.4).
	expect(
		pressables(tree)
			.map((node) => node.props.accessibilityLabel)
			.slice(0, 2),
	).toEqual(["All", "Live"]);
	act(() => labelled(tree, "Search for docs").props.onPress());
	expect(props.onRecent).toHaveBeenCalledWith("docs");
	const clear = labelled(tree, "Clear recent searches");
	expect(flatten(textWith(clear, "Clear").props.style).color).toBe(palette.accentInk);
	act(() => clear.props.onPress());
	expect(props.onClearRecent).toHaveBeenCalled();
});

it("shows only the scope chips when the field is empty and there are no recent searches", () => {
	const { tree, props } = mount({ search: { query: "", results: null, searching: false, failed: false } });
	expect(pressables(tree).map((node) => node.props.accessibilityLabel)).toEqual(["All", "Live"]);
	expect(renderedText(tree)).not.toContain("RECENT");
	// A scope can be picked before there's a query.
	act(() => labelled(tree, "Live").props.onPress());
	expect(props.onScope).toHaveBeenCalledWith("live");
});

it("names an untitled session", () => {
	const { tree } = mount({ search: { ...answered, results: { live: [result("x", { title: "" })], past: [] } } });
	expect(resultRows(tree)[0].props.accessibilityLabel).toBe("Untitled session, evener, 2 minutes");
});

it("lists the matching projects after the sessions, each opening its project", () => {
	const { tree, props } = mount({ projects: [evener, unnamed] });
	const text = renderedText(tree);
	expect(text.indexOf("PROJECTS · 2")).toBeGreaterThan(text.indexOf("SESSIONS · 3"));
	expect(projectRows(tree).map((row) => row.props.accessibilityLabel)).toEqual([
		"evener, project, /home/jesse/git/evener",
		// A project with no name is named by its folder, said once.
		"/srv/tools, project",
	]);
	expect(flatten(projectRows(tree)[0].props.style).minHeight).toBeGreaterThanOrEqual(44);
	act(() => projectRows(tree)[0].props.onPress());
	expect(props.onOpenProject).toHaveBeenCalledWith(evener);
});

it("shows no Projects group in the Live scope", () => {
	const { tree } = mount({ scope: "live", projects: [evener] });
	expect(projectRows(tree)).toHaveLength(0);
	expect(renderedText(tree)).not.toContain("PROJECTS");
});

it("lists matching projects whatever the hub's search is doing", () => {
	const states: SearchSnapshot[] = [
		{ query: "even", results: null, searching: true, failed: false },
		{ query: "even", results: null, searching: false, failed: true },
		{ query: "even", results: { live: [], past: [] }, searching: false, failed: false },
	];
	for (const search of states) {
		const { tree } = mount({ search, projects: [evener] });
		expect(projectRows(tree)).toHaveLength(1);
		act(() => tree.unmount());
	}
	const offline = mount({ connected: false, search: states[0], projects: [evener] });
	expect(projectRows(offline.tree)).toHaveLength(1);
});

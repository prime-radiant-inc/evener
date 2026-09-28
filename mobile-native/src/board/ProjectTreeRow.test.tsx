import type { NavigationProjectSummary } from "@evener/appwire-client";
import type { ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { type ProjectRowItem, ProjectTreeRow } from "./ProjectTreeRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);

const project = (over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 0,
	...over,
});
const laptop = { id: "local", label: "Laptop", online: true };
const park = { id: "paradise-park", label: "paradise-park", online: false };

function draw(item: ProjectRowItem, onPress = () => {}) {
	return render(<ProjectTreeRow item={item} onPress={onPress} />);
}
const texts = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => node.type === ("Text" as never))
		.flatMap((node) => [node.props.children].flat().filter((child): child is string => typeof child === "string"));
const symbols = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name);
const pressables = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.type === ("Pressable" as never));

describe("host rows", () => {
	it("says an offline host is Offline, with no count", () => {
		const tree = draw({ kind: "host", key: "h", fold: "host:paradise-park", depth: 0, host: park, liveCount: null, folded: false });
		expect(texts(tree)).toEqual(["paradise-park", "Offline"]);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.down"]);
	});

	it("counts a connected host's live sessions", () => {
		const tree = draw({ kind: "host", key: "h", fold: "host:local", depth: 0, host: laptop, liveCount: 3, folded: true });
		expect(texts(tree)).toEqual(["Laptop", "3 live"]);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.right"]);
	});

	it("shows no state for a connected host whose count isn't known", () => {
		const tree = draw({ kind: "host", key: "h", fold: "host:local", depth: 0, host: laptop, liveCount: null, folded: true });
		expect(texts(tree)).toEqual(["Laptop"]);
	});
});

describe("project rows", () => {
	it("puts a pin before a pinned project's name", () => {
		const item: ProjectRowItem = {
			kind: "project",
			key: "p",
			fold: "project:evener",
			depth: 0,
			project: project({ favorite: true }),
			liveCount: 2,
			folded: true,
		};
		const tree = draw(item);
		expect(symbols(tree)).toEqual(["folder", "pin.fill", "chevron.right"]);
		expect(texts(tree)).toEqual(["evener", "2 live"]);
	});

	it("draws no pin for an unpinned project, and names an unnamed one by its directory", () => {
		const item: ProjectRowItem = {
			kind: "project",
			key: "p",
			fold: "project:x",
			depth: 1,
			project: project({ name: "" }),
			liveCount: null,
			folded: false,
		};
		const tree = draw(item);
		expect(symbols(tree)).toEqual(["folder", "chevron.down"]);
		expect(texts(tree)).toEqual(["/home/jesse/git/evener"]);
	});

	it("opens its menu on a long press", () => {
		const onLongPress = vi.fn();
		const item: ProjectRowItem = { kind: "project", key: "p", fold: "f", depth: 0, project: project(), liveCount: null, folded: true };
		const tree = render(<ProjectTreeRow item={item} onPress={() => {}} onLongPress={onLongPress} />);
		act(() => pressables(tree)[0].props.onLongPress());
		expect(onLongPress).toHaveBeenCalledTimes(1);
	});

	it("dims while a change to the project is on its way", () => {
		const item: ProjectRowItem = { kind: "project", key: "p", fold: "f", depth: 0, project: project(), liveCount: null, folded: true };
		const opacity = (changing: boolean) => {
			const tree = render(<ProjectTreeRow item={item} onPress={() => {}} changing={changing} />);
			return pressables(tree)[0].props.style({ pressed: false }).opacity;
		};
		expect(opacity(false)).toBe(1);
		expect(opacity(true)).toBe(0.5);
	});
});

describe("the rows inside a project", () => {
	it("names a branch's host, and says Offline when it is", () => {
		const tree = draw({ kind: "branch", key: "b", fold: "b", depth: 1, host: park, folded: true });
		expect(texts(tree)).toEqual(["paradise-park", "Offline"]);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.right"]);
	});

	it("captions a tier", () => {
		const tree = draw({ kind: "tier", key: "t", depth: 1, label: "Today" });
		expect(texts(tree)).toEqual(["Today"]);
		expect(pressables(tree)).toHaveLength(0);
	});

	it("counts the archived group once its count is known", () => {
		expect(texts(draw({ kind: "archivedGroup", key: "a", fold: "a", depth: 1, count: 12, folded: true }))).toEqual([
			"Archived · 12",
		]);
		expect(texts(draw({ kind: "archivedGroup", key: "a", fold: "a", depth: 1, count: null, folded: true }))).toEqual([
			"Archived",
		]);
	});

	it("loads more when its more row is pressed", () => {
		const onPress = vi.fn();
		const tree = draw({ kind: "more", key: "m", depth: 1, projectKey: "evener", tier: "recent", remaining: 12 }, onPress);
		expect(texts(tree)).toEqual(["12 more"]);
		act(() => pressables(tree)[0].props.onPress());
		expect(onPress).toHaveBeenCalledTimes(1);
	});

	it("reads more projects when the section's last row is pressed", () => {
		const onPress = vi.fn();
		const tree = draw({ kind: "moreProjects", key: "m", depth: 0, remaining: 70 }, onPress);
		expect(texts(tree)).toEqual(["70 more projects"]);
		expect(texts(draw({ kind: "moreProjects", key: "m", depth: 0, remaining: 1 }))).toEqual(["1 more project"]);
		const [row] = pressables(tree);
		expect(row.props.accessibilityLabel).toBe("70 more projects");
		expect(row.props.style({ pressed: false }).minHeight).toBe(44);
		act(() => row.props.onPress());
		expect(onPress).toHaveBeenCalledTimes(1);
	});

	it("draws a still placeholder while a tier loads", () => {
		const tree = draw({ kind: "loading", key: "l", depth: 1 });
		expect(tree.root.findAll((node) => node.props.testID === "project-loading")).toHaveLength(1);
		expect(pressables(tree)).toHaveLength(0);
	});

	it("says a failed read failed, with nothing to press", () => {
		const tree = draw({ kind: "failed", key: "f", depth: 1 });
		expect(texts(tree)).toEqual(["Couldn't load these sessions."]);
		expect(pressables(tree)).toHaveLength(0);
	});
});

it("reports whether each folding row is open", () => {
	const folding: ProjectRowItem[] = [
		{ kind: "host", key: "h", fold: "h", depth: 0, host: laptop, liveCount: null, folded: true },
		{ kind: "project", key: "p", fold: "p", depth: 0, project: project(), liveCount: null, folded: false },
		{ kind: "branch", key: "b", fold: "b", depth: 1, host: laptop, folded: true },
		{ kind: "archivedGroup", key: "a", fold: "a", depth: 1, count: 3, folded: false },
	];
	for (const item of folding) {
		const [row] = pressables(draw(item));
		expect(row.props.accessibilityRole).toBe("button");
		expect(row.props.accessibilityState).toEqual({ expanded: !("folded" in item && item.folded) });
	}
});

it("indents each row 16pt per depth after the 16pt margin", () => {
	const tree = draw({ kind: "tier", key: "t", depth: 2, label: "Recent" });
	const flat = tree.root.findAll((node) => node.type === ("View" as never) && node.props.style?.paddingLeft !== undefined);
	expect(flat[0].props.style.paddingLeft).toBe(16 + 2 * 16);
});

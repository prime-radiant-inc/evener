import { isDisclosureOpenIn } from "@evener/appwire-client";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MobileConversation } from "../projectedRows";
import { TimelineItem } from "../TimelineItem";
import { nativeDisclosureStore } from "../nativeDisclosure";
import { render, textOf, unmountMountedTrees } from "../renderNative.testkit";
import type { TimelineRow } from "../timeline";
import { rowDisclosureIds } from "./disclosureKeys";
import type { FindState } from "./findInSession";
import { useFindInSession } from "./useFindInSession";

vi.hoisted(() => {
	(globalThis as { __DEV__?: boolean }).__DEV__ = false;
});

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => true }));
vi.mock("expo-secure-store", () => ({
	getItemAsync: async () => null,
	setItemAsync: async () => undefined,
	deleteItemAsync: async () => undefined,
}));
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite/kv-store", () => {
	const values = new Map<string, string>();
	const Storage = {
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => void values.set(key, value),
		removeItemSync: (key: string) => void values.delete(key),
	};
	return { default: Storage, Storage };
});

const HUB_ID = "hub-1";
const SESSION_REF = "local:test";

function hasRenderedProse(tree: ReactTestRenderer, needle: string): boolean {
	const hasMarkdown = tree.root
		.findAll((node) => String(node.type) === "EnrichedMarkdownText")
		.some((node) => typeof node.props.markdown === "string" && node.props.markdown.includes(needle));
	const hasText = tree.root
		.findAll((node) => String(node.type) === "Text")
		.some((node) => textOf(node).includes(needle));
	return hasMarkdown || hasText;
}

function isRowOpen(row: TimelineRow): boolean {
	const id = rowDisclosureIds(HUB_ID, SESSION_REF, row)[0] ?? "";
	return isDisclosureOpenIn(nativeDisclosureStore.getState(), id, false);
}

function Harness({ rows, initialFind }: { rows: readonly TimelineRow[]; initialFind: FindState | null }) {
	const conversation = {
		ref: SESSION_REF,
		turns: [],
		items: [],
	} as unknown as MobileConversation;
	useFindInSession({
		hubId: HUB_ID,
		sessionRef: SESSION_REF,
		find: initialFind,
		setFind: () => {},
		timelineRows: rows,
		answersByRow: new Map(),
		answerStandIns: new Map(),
		conversation,
		focused: true,
		status: "open",
		loadingOlder: false,
		olderPage: null,
		service: {},
		connected: true,
		olderHistory: { permanent: false },
		isCurrentReader: () => true,
		findOlder: () => {},
		follow: { state: { current: { following: true } }, dispatch: () => {} } as never,
		timeline: { current: { scrollToIndex: () => {} } } as never,
		listOffset: { current: 0 },
		readerContentHeight: { current: 0 },
		readerViewportHeight: { current: 0 },
		listUnderBar: {},
		captureSuppressed: { current: false },
	});
	return (
		<>
			{rows.map((row) => (
				<TimelineItem key={row.id} item={row} hubId={HUB_ID} sessionRef={SESSION_REF} />
			))}
		</>
	);
}

describe("useFindInSession disclosure opening for quiet yields", () => {
	afterEach(() => {
		unmountMountedTrees();
		nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
	});

	it("opens a collapsed quiet-yield group and child commentary notice when Find selects a commentary match", () => {
		const childNoProse: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_1",
			turnId: "t1",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			text: "Ended turn quietly",
		};
		const childWithProse: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_2",
			turnId: "t2",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "label-sentinel",
			text: "quokka-grouped-prose",
			rendersMarkdown: true,
		};
		const groupRow: TimelineRow = {
			kind: "details",
			id: "quiet-yields:qy_1",
			entries: [childNoProse, childWithProse],
			summary: "2 quiet yields",
		};
		const rows: TimelineRow[] = [groupRow];

		let tree!: ReactTestRenderer;
		act(() => {
			tree = render(<Harness rows={rows} initialFind={null} />);
		});
		expect(isRowOpen(groupRow)).toBe(false);
		expect(isRowOpen(childWithProse)).toBe(false);
		expect(hasRenderedProse(tree, "quokka-grouped-prose")).toBe(false);

		act(() => {
			tree.update(
				<Harness
					rows={rows}
					initialFind={{ query: "quokka-grouped-prose", key: "quiet-yields:qy_1", seeking: false, exhausted: false }}
				/>,
			);
		});
		expect(isRowOpen(groupRow)).toBe(true);
		expect(isRowOpen(childWithProse)).toBe(true);
		expect(hasRenderedProse(tree, "quokka-grouped-prose")).toBe(true);
	});

	it("opens only the group when Find matches a child notice label inside a quiet-yield group", () => {
		const childWithProse: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_lbl_1",
			turnId: "t1",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "child-label-sentinel",
			text: "hidden-prose-body",
			rendersMarkdown: true,
		};
		const childTwo: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_lbl_2",
			turnId: "t2",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			text: "other-yield",
		};
		const groupRow: TimelineRow = {
			kind: "details",
			id: "quiet-yields:qy_lbl_1",
			entries: [childWithProse, childTwo],
			summary: "2 quiet yields",
		};

		let tree!: ReactTestRenderer;
		act(() => {
			tree = render(
				<Harness
					rows={[groupRow]}
					initialFind={{
						query: "child-label-sentinel",
						key: "quiet-yields:qy_lbl_1",
						seeking: false,
						exhausted: false,
					}}
				/>,
			);
		});
		expect(isRowOpen(groupRow)).toBe(true);
		expect(isRowOpen(childWithProse)).toBe(false);
		expect(hasRenderedProse(tree, "child-label-sentinel")).toBe(true);
		expect(hasRenderedProse(tree, "hidden-prose-body")).toBe(false);
	});

	it("opens a standalone quiet-yield notice disclosure only when Find matches its collapsed commentary", () => {
		const standalone: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_single",
			turnId: "t_single",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "standalone-label-sentinel",
			text: "quokka-standalone-prose",
			rendersMarkdown: true,
		};
		const rows: TimelineRow[] = [standalone];

		let tree!: ReactTestRenderer;
		act(() => {
			tree = render(<Harness rows={rows} initialFind={null} />);
		});
		expect(isRowOpen(standalone)).toBe(false);
		expect(hasRenderedProse(tree, "quokka-standalone-prose")).toBe(false);

		act(() => {
			tree.update(
				<Harness
					rows={rows}
					initialFind={{ query: "quokka-standalone-prose", key: "qy_single", seeking: false, exhausted: false }}
				/>,
			);
		});
		expect(isRowOpen(standalone)).toBe(true);
		expect(hasRenderedProse(tree, "quokka-standalone-prose")).toBe(true);
	});

	it("opens a collapsed quiet-yield group when Find selects a round_timings child inside it", () => {
		const timingChild: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "rt_1",
			turnId: "t1",
			origin: "system",
			family: "diagnostic",
			tone: "system",
			eventKind: "round_timings",
			text: "quokka-timing-120ms",
		};
		const q1: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_1",
			turnId: "t1",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			text: "Ended turn quietly",
		};
		const q2: Extract<TimelineRow, { kind: "notice" }> = { ...q1, id: "qy_2", turnId: "t2" };
		const groupRow: TimelineRow = {
			kind: "details",
			id: "quiet-yields:qy_1",
			entries: [q1, timingChild, q2],
			summary: "2 quiet yields",
		};
		let tree!: ReactTestRenderer;
		act(() => {
			tree = render(
				<Harness
					rows={[groupRow]}
					initialFind={{ query: "quokka-timing-120ms", key: "quiet-yields:qy_1", seeking: false, exhausted: false }}
				/>,
			);
		});
		expect(isRowOpen(groupRow)).toBe(true);
		expect(hasRenderedProse(tree, "quokka-timing-120ms")).toBe(true);
	});
});

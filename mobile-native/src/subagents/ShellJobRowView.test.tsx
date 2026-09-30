// A shell job's row in the Activity list (Jesse's ruling on shell jobs): a $
// glyph whose hue is the state, the description, who started it, and its
// status with its age while running or its duration once finished.
import type { ActivityJob } from "@evener/appwire-client";
import { describe, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import type { ShellJobRow } from "./subagentModel";
import { ShellJobRowView } from "./ShellJobRowView";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const NOW = Date.UTC(2026, 8, 29, 12, 0, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const job = (over: Partial<ActivityJob> = {}): ActivityJob => ({
	jobId: "job-1",
	ownerSessionId: "coord",
	ownerRef: "local:coord",
	type: "shell",
	status: "running",
	terminal: false,
	background: true,
	hasOutput: true,
	description: "Serving the docs",
	command: "npm run docs",
	startedAt: ago(4 * 60_000),
	outputBytes: 0,
	...over,
});
const row = (state: ShellJobRow["state"], over: Partial<ActivityJob> = {}): ShellJobRow => ({
	kind: "job",
	id: "job-1",
	title: "Serving the docs",
	owner: "Fix race in tree settle",
	state,
	job: job(over),
	order: 0,
});
const glyphColor = (tree: ReturnType<typeof render>) =>
	tree.root.find((node) => String(node.type) === "Text" && node.props.children === "$").props.style.color;

describe("a shell job's row", () => {
	it("says what it is, who started it, and how long it has been quiet while it runs", () => {
		const tree = render(<ShellJobRowView row={row("running", { lastOutputAt: ago(90_000) })} now={NOW} />);
		expect(renderedText(tree)).toContain("Serving the docs");
		expect(renderedText(tree)).toContain("under Fix race in tree settle");
		expect(renderedText(tree)).toContain("running · 1m");
		expect(glyphColor(tree)).toBe("#12763B");
	});

	it("says how long a finished one ran, its hue carrying the outcome", () => {
		const failed = render(
			<ShellJobRowView
				row={row("failed", { terminal: true, status: "command_exited_nonzero", endedAt: ago(60_000) })}
				now={NOW}
			/>,
		);
		expect(renderedText(failed)).toContain("3m");
		expect(glyphColor(failed)).toBe("#C51D23");
		const done = render(<ShellJobRowView row={row("done", { terminal: true, endedAt: ago(60_000) })} now={NOW} />);
		expect(glyphColor(done)).toBe("#6D6D64");
	});

	it("reads to VoiceOver as one sentence", () => {
		const tree = render(<ShellJobRowView row={row("running")} now={NOW} />);
		expect(tree.root.find((node) => typeof node.props.accessibilityLabel === "string").props.accessibilityLabel).toBe(
			"Shell job, Serving the docs, running · 4m, under Fix race in tree settle",
		);
	});
});

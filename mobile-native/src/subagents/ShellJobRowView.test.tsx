// A shell job's row in the Activity list (Jesse's ruling on shell jobs): a $
// glyph whose hue is the state, the description, who started it, and its
// status with its age while running or its duration once finished.
import type { ActivityJob } from "@evener/appwire-client";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
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
const palette = paletteFor("light");
const label = (tree: ReturnType<typeof render>) =>
	tree.root.find((node) => typeof node.props.accessibilityLabel === "string").props.accessibilityLabel;
const glyphColor = (tree: ReturnType<typeof render>) =>
	tree.root.find((node) => String(node.type) === "Text" && node.props.children === "$").props.style.color;

describe("a shell job's row", () => {
	it("says what it is, who started it, and how long it has been quiet while it runs", () => {
		const tree = render(
			<ShellJobRowView row={row("running", { lastOutputAt: ago(90_000) })} now={NOW} onOpen={() => {}} />,
		);
		expect(renderedText(tree)).toContain("Serving the docs");
		expect(renderedText(tree)).toContain("under Fix race in tree settle");
		expect(renderedText(tree)).toContain("running · 1m");
		expect(glyphColor(tree)).toBe(palette.aliveInk);
	});

	// An ended job says how it ended in words as well as in its hue, so a
	// failed, killed, stopped and finished job never read the same.
	it("says how a finished one ended and how long it ran, its hue carrying the outcome too", () => {
		const ended = (state: ShellJobRow["state"], status: string, outcome: string) =>
			render(
				<ShellJobRowView
					row={row(state, { terminal: true, status, outcome, endedAt: ago(60_000) })}
					now={NOW}
					onOpen={() => {}}
				/>,
			);
		const failed = ended("failed", "command_exited_nonzero", "failure");
		expect(renderedText(failed)).toContain("Command failed · 3m");
		expect(label(failed)).toBe("Shell job, Serving the docs, Command failed, 3 minutes, under Fix race in tree settle");
		expect(glyphColor(failed)).toBe(palette.dangerInk);
		expect(label(ended("failed", "command_killed", "failure"))).toContain("Command killed, 3 minutes");
		expect(label(ended("failed", "exhausted", "failure"))).toContain("Failed, 3 minutes");
		expect(label(ended("done", "stopped", "neutral"))).toContain("Stopped, 3 minutes");
		const done = ended("done", "completed", "success");
		expect(renderedText(done)).toContain("3m");
		expect(renderedText(done)).not.toContain("Done");
		expect(label(done)).toBe("Shell job, Serving the docs, Done, 3 minutes, under Fix race in tree settle");
		expect(glyphColor(done)).toBe(palette.inkLow);
	});

	it("opens the job it shows", () => {
		const onOpen = vi.fn();
		const shown = row("running");
		const tree = render(<ShellJobRowView row={shown} now={NOW} onOpen={onOpen} />);
		tree.root.find((node) => String(node.type) === "Pressable").props.onPress();
		expect(onOpen).toHaveBeenCalledWith(shown);
	});

	it("reads to VoiceOver as one sentence", () => {
		const tree = render(<ShellJobRowView row={row("running")} now={NOW} onOpen={() => {}} />);
		expect(label(tree)).toBe("Shell job, Serving the docs, running, 4 minutes, under Fix race in tree settle");
	});
});

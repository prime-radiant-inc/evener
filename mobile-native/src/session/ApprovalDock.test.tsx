// The approval dock as a person sees it (spec 8.4, rulings 12 and 38): what
// the sandbox blocked, Allow and Deny, and what happens after each. The
// controls are a double in ApprovalControls' shape; ApprovalControls itself
// is covered by approvals.test.ts.
import type { SandboxEscalationRequested } from "@evener/appwire-client";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalControls } from "../approvalControls";
import { pressable, render, renderedText } from "../renderNative.testkit";
import { ApprovalDock } from "./ApprovalDock";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

type State = ReturnType<ApprovalControls["getSnapshot"]>;

/** ApprovalControls' surface: a decision settles into `outcome`, and a
 * refresh into `refreshed`. */
function fakeControls(initial: Partial<State> = {}, outcome: Partial<State> = {}, refreshed: Partial<State> = {}) {
	let state: State = { pending: null, refreshing: false, error: null, ...initial };
	const listeners = new Set<() => void>();
	const publish = (next: Partial<State>) => {
		state = { ...state, ...next };
		for (const listener of listeners) listener();
	};
	const controls = {
		getSnapshot: () => state,
		subscribe: (listener: () => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		resolve: vi.fn(async (request: SandboxEscalationRequested) => {
			publish({ pending: request.escalationId });
			await Promise.resolve();
			publish({ pending: null, refreshing: false, error: null, ...outcome });
		}),
		refresh: vi.fn(async () => {
			publish({ refreshing: true });
			await Promise.resolve();
			publish({ refreshing: false, ...refreshed });
		}),
	};
	return { controls, asControls: controls as unknown as ApprovalControls, publish };
}

const request = (over: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested => ({
	threadId: "thread-1",
	ref: "local:s1",
	escalationId: "esc-1",
	mode: "workspace-write",
	tool: "write_file",
	kind: "file_tool",
	deniedPath: "/Users/jesse/sites/docs/index.html",
	...over,
});

function mount(approval: SandboxEscalationRequested, fake: ReturnType<typeof fakeControls>) {
	const onDecided = vi.fn<(allowed: boolean) => void>();
	const tree = render(<ApprovalDock request={approval} controls={fake.asControls} onDecided={onDecided} />);
	return { tree, onDecided };
}

async function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	expect(target.props.accessibilityState).toMatchObject({ disabled: false });
	await act(async () => {
		target.props.onPress();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

const readable = (tree: ReactTestRenderer) => renderedText(tree).replaceAll("\u200b", "");

describe("the approval dock (spec 8.4)", () => {
	it("says what the sandbox blocked, and offers the one file", () => {
		const { tree } = mount(request(), fakeControls());
		const text = readable(tree);
		expect(text).toContain("Wants to write outside the workspace");
		expect(text).toContain("write_file  /Users/jesse/sites/docs/index.html");
		expect(text).toContain("This session can only write inside its project folder.");
		expect(text).toContain("Allow this file only");
		expect(text).toContain("It will ask again for the next one");
		expect(text).toContain("Deny");
		expect(text).not.toContain("Part of this may already have run.");
		// A long path wraps only at its slashes.
		expect(renderedText(tree)).toContain("/\u200bUsers/\u200bjesse/\u200b");
	});

	it("allows once when the hub couldn't name the path, and says part of it ran", () => {
		const { tree } = mount(request({ deniedPath: "<denied>", partiallyRan: true }), fakeControls());
		const text = readable(tree);
		expect(text).toContain("Allow once");
		expect(text).toContain("Just this action");
		expect(text).not.toContain("<denied>");
		expect(text).toContain("This session can only write inside its project folder. Part of this may already have run.");
	});

	it("without controls, says what waits and offers nothing to press", () => {
		const onDecided = vi.fn();
		const tree = render(<ApprovalDock request={request()} controls={null} onDecided={onDecided} />);
		const text = readable(tree);
		expect(text).toContain("Wants to write outside the workspace");
		expect(text).toContain("This session can only write inside its project folder.");
		expect(pressable(tree, "Allow this file only")).toBeUndefined();
		expect(pressable(tree, "Deny")).toBeUndefined();
		expect(text).not.toContain("It will ask again for the next one");
	});

	it("allows, and reports the decision once it settles", async () => {
		const fake = fakeControls();
		const approval = request();
		const { tree, onDecided } = mount(approval, fake);
		await press(tree, "Allow this file only");
		expect(fake.controls.resolve).toHaveBeenCalledWith(approval, true);
		expect(onDecided).toHaveBeenCalledWith(true);
	});

	it("denies", async () => {
		const fake = fakeControls();
		const approval = request();
		const { tree, onDecided } = mount(approval, fake);
		await press(tree, "Deny");
		expect(fake.controls.resolve).toHaveBeenCalledWith(approval, false);
		expect(onDecided).toHaveBeenCalledWith(false);
	});

	it("keeps a decision already sent pending when a reconnect swaps the controls (no second request)", async () => {
		const approval = request();
		// The first controls send the decision, then are disposed by the
		// reconnect: their resolve settles without publishing anything more.
		const first = fakeControls();
		let settle = () => {};
		first.controls.resolve.mockImplementation(async (sent: SandboxEscalationRequested) => {
			first.publish({ pending: sent.escalationId });
			await new Promise<void>((resolve) => {
				settle = resolve;
			});
		});
		const onDecided = vi.fn<(allowed: boolean) => void>();
		const tree = render(<ApprovalDock request={approval} controls={first.asControls} onDecided={onDecided} />);
		await press(tree, "Allow this file only");
		expect(first.controls.resolve).toHaveBeenCalledTimes(1);
		// The reconnect's new controls know nothing of that decision.
		const second = fakeControls();
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={second.asControls} onDecided={onDecided} />);
		});
		await act(async () => {
			settle();
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		for (const label of ["Allow this file only", "Deny"]) {
			const button = pressable(tree, label);
			expect(button?.props.accessibilityState).toMatchObject({ disabled: true });
			await act(async () => button?.props.onPress());
		}
		expect(second.controls.resolve).not.toHaveBeenCalled();
		expect(first.controls.resolve).toHaveBeenCalledTimes(1);
		// Nobody confirmed it, so it says nothing was decided.
		expect(onDecided).not.toHaveBeenCalled();
	});

	it("lets you decide again once the new controls report an error for it", async () => {
		const approval = request();
		const first = fakeControls();
		first.controls.resolve.mockImplementation(async (sent: SandboxEscalationRequested) => {
			first.publish({ pending: sent.escalationId });
			await new Promise<void>(() => {});
		});
		const tree = render(<ApprovalDock request={approval} controls={first.asControls} onDecided={vi.fn()} />);
		await press(tree, "Allow this file only");
		const second = fakeControls({}, {}, {});
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={second.asControls} onDecided={vi.fn()} />);
		});
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
		await act(async () => {
			second.publish({ error: "Couldn't confirm your decision. It may already have been applied." });
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		// The dock re-reads once; the read clears the error.
		expect(second.controls.refresh).toHaveBeenCalledTimes(1);
		await act(async () => {
			second.publish({ error: null });
		});
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: false });
	});

	it("holds both buttons while a decision is on its way", () => {
		const { tree } = mount(request(), fakeControls({ pending: "esc-1" }));
		expect(pressable(tree, "Allow this file only")?.props.accessibilityState).toMatchObject({ disabled: true });
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
	});

	it("shows a decision it couldn't confirm, reports none, and re-reads the session once on its own", async () => {
		const error = "Couldn't confirm your decision. It may already have been applied.";
		// The re-read fails too, so the error stays.
		const fake = fakeControls({}, { error }, { error });
		const { tree, onDecided } = mount(request(), fake);
		await press(tree, "Allow this file only");
		expect(onDecided).not.toHaveBeenCalled();
		expect(renderedText(tree)).toContain(error);
		expect(fake.controls.refresh).toHaveBeenCalledTimes(1);
		await act(async () => {
			fake.publish({});
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		expect(fake.controls.refresh).toHaveBeenCalledTimes(1);
		expect(pressable(tree, "Refresh session")).toBeUndefined();
	});
});

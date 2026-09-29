// The approval dock as a person sees it (spec 8.4, rulings 12 and 38): what
// the sandbox blocked, Allow and Deny, and what happens after each. The
// controls are a double in ApprovalControls' shape; ApprovalControls itself
// is covered by approvals.test.ts.
import type { SandboxEscalationRequested } from "@evener/appwire-client";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalControls } from "../approvalControls";
import { dockBody, pressable, render, renderedText, textOf } from "../renderNative.testkit";
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
	it("scrolls what the sandbox blocked when the screen has less room, while Allow and Deny stay put", () => {
		const { tree } = mount(request(), fakeControls());
		const body = dockBody(tree, "approval-dock");
		const scrolled = textOf(body.scroller).replaceAll("\u200b", "");
		expect(scrolled).toContain("Wants to write outside the workspace");
		expect(scrolled).toContain("write_file  /Users/jesse/sites/docs/index.html");
		expect(scrolled).toContain("This session can only write inside its project folder.");
		for (const label of ["Allow this file only", "Deny"]) expect(body.holds(label)).toBe(false);
	});

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

	it("says how many more approvals wait behind this one", () => {
		const one = render(
			<ApprovalDock request={request()} controls={fakeControls().asControls} onDecided={vi.fn()} waiting={1} />,
		);
		expect(renderedText(one)).toContain("1 more waiting");
		const three = render(
			<ApprovalDock request={request()} controls={fakeControls().asControls} onDecided={vi.fn()} waiting={3} />,
		);
		expect(renderedText(three)).toContain("3 more waiting");
		const alone = render(<ApprovalDock request={request()} controls={fakeControls().asControls} onDecided={vi.fn()} />);
		expect(renderedText(alone)).not.toContain("waiting");
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
		// The reconnect's new controls know nothing of that decision, and their
		// read of the session is still on its way.
		const second = fakeControls();
		second.controls.refresh.mockImplementation(() => new Promise<void>(() => {}));
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

	it("lets you decide again when the fresh read after a reconnect still lists the approval", async () => {
		const approval = request();
		const first = fakeControls();
		first.controls.resolve.mockImplementation(async (sent: SandboxEscalationRequested) => {
			first.publish({ pending: sent.escalationId });
			await new Promise<void>(() => {});
		});
		const tree = render(<ApprovalDock request={approval} controls={first.asControls} onDecided={vi.fn()} />);
		await press(tree, "Allow this file only");
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
		// The new controls re-read the session; the approval is still listed
		// (the screen still renders this dock for it), so the first decision
		// can't be known to have landed.
		const second = fakeControls();
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={second.asControls} onDecided={vi.fn()} />);
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		expect(second.controls.refresh).toHaveBeenCalledTimes(1);
		await press(tree, "Deny");
		expect(second.controls.resolve).toHaveBeenCalledWith(approval, false);
		expect(first.controls.resolve).toHaveBeenCalledTimes(1);
	});

	it("lets only the latest controls' read decide after two reconnects in a row", async () => {
		const approval = request();
		const first = fakeControls();
		first.controls.resolve.mockImplementation(async (sent: SandboxEscalationRequested) => {
			first.publish({ pending: sent.escalationId });
			await new Promise<void>(() => {});
		});
		const tree = render(<ApprovalDock request={approval} controls={first.asControls} onDecided={vi.fn()} />);
		await press(tree, "Allow this file only");
		// Each reconnect's read settles only when the test says so.
		const deferredRead = (fake: ReturnType<typeof fakeControls>) => {
			let settle = () => {};
			fake.controls.refresh.mockImplementation(
				() =>
					new Promise<void>((resolve) => {
						settle = resolve;
					}),
			);
			return () => settle();
		};
		const second = fakeControls();
		const settleSecond = deferredRead(second);
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={second.asControls} onDecided={vi.fn()} />);
		});
		const third = fakeControls();
		const settleThird = deferredRead(third);
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={third.asControls} onDecided={vi.fn()} />);
		});
		expect(third.controls.refresh).toHaveBeenCalledTimes(1);
		// The replaced controls' read says nothing about the hub now.
		await act(async () => {
			settleSecond();
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
		// The current controls' read still lists the approval: decide again.
		await act(async () => {
			settleThird();
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: false });
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
		second.controls.refresh.mockImplementationOnce(() => new Promise<void>(() => {}));
		await act(async () => {
			tree.update(<ApprovalDock request={approval} controls={second.asControls} onDecided={vi.fn()} />);
		});
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
		await act(async () => {
			second.publish({ error: "Couldn't confirm your decision. It may already have been applied." });
			await new Promise((resolve) => setTimeout(resolve, 0));
		});
		// One read after the swap, and one for the error; the read clears it.
		expect(second.controls.refresh).toHaveBeenCalledTimes(2);
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

	it("brings Allow and Deny back once its re-read settles, even when the error stands, and a press tries again", async () => {
		const error = "Couldn't confirm your decision. It may already have been applied.";
		// The decision fails, and so does the re-read after it.
		const fake = fakeControls({}, { error }, { error });
		const approval = request();
		const { tree } = mount(approval, fake);
		await press(tree, "Allow this file only");
		expect(fake.controls.refresh).toHaveBeenCalledTimes(1);
		expect(renderedText(tree)).toContain(error);
		for (const label of ["Allow this file only", "Deny"])
			expect(pressable(tree, label)?.props.accessibilityState).toMatchObject({ disabled: false });
		await press(tree, "Deny");
		expect(fake.controls.resolve).toHaveBeenCalledTimes(2);
		expect(fake.controls.resolve).toHaveBeenLastCalledWith(approval, false);
	});

	it("holds Allow and Deny while its re-read is on its way", async () => {
		const error = "Couldn't confirm your decision. It may already have been applied.";
		const fake = fakeControls({}, { error });
		fake.controls.refresh.mockImplementation(async () => {
			fake.publish({ refreshing: true });
			await new Promise<void>(() => {});
		});
		const { tree } = mount(request(), fake);
		await press(tree, "Allow this file only");
		expect(pressable(tree, "Deny")?.props.accessibilityState).toMatchObject({ disabled: true });
	});
});

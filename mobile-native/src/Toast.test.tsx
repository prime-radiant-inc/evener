import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pressable, render, renderHook, renderedText, unmountMountedTrees } from "./renderNative.testkit";
import { TOAST_ACTION_MS, TOAST_MS, Toast, useToast } from "./Toast";

const announce = vi.hoisted(() => vi.fn());
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: announce },
}));

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
	unmountMountedTrees();
	vi.useRealTimers();
});

it("shows a toast, announces it once, and clears it after four seconds", () => {
	const hook = renderHook(() => useToast());
	act(() => hook.result.current.show({ text: "Stopped" }));
	expect(hook.result.current.toast?.text).toBe("Stopped");
	expect(announce).toHaveBeenCalledWith("Stopped");
	act(() => {
		vi.advanceTimersByTime(TOAST_MS - 1);
	});
	expect(hook.result.current.toast?.text).toBe("Stopped");
	act(() => {
		vi.advanceTimersByTime(1);
	});
	expect(hook.result.current.toast).toBeNull();
});

it("keeps a toast that offers an action for eight seconds", () => {
	const hook = renderHook(() => useToast());
	act(() => hook.result.current.show({ text: "Session archived", action: { label: "Undo", run: () => {} } }));
	act(() => {
		vi.advanceTimersByTime(TOAST_MS);
	});
	expect(hook.result.current.toast?.text).toBe("Session archived");
	act(() => {
		vi.advanceTimersByTime(TOAST_ACTION_MS - TOAST_MS);
	});
	expect(hook.result.current.toast).toBeNull();
});

it("lets a newer toast replace an older one without the older timer clearing it", () => {
	const hook = renderHook(() => useToast());
	act(() => hook.result.current.show({ text: "Stopped" }));
	act(() => {
		vi.advanceTimersByTime(TOAST_MS - 100);
	});
	act(() => hook.result.current.show({ text: "Note saved" }));
	act(() => {
		vi.advanceTimersByTime(200);
	});
	expect(hook.result.current.toast?.text).toBe("Note saved");
});

it("runs the action and dismisses when its button is pressed", () => {
	const run = vi.fn();
	const dismiss = vi.fn();
	const tree = render(
		<Toast toast={{ id: 1, text: "Session archived", action: { label: "Undo", run } }} dismiss={dismiss} />,
	);
	expect(renderedText(tree)).toContain("Session archived");
	act(() => pressable(tree, "Undo")?.props.onPress());
	expect(run).toHaveBeenCalledOnce();
	expect(dismiss).toHaveBeenCalledOnce();
});

it("renders nothing without a toast", () => {
	expect(render(<Toast toast={null} dismiss={() => {}} />).toJSON()).toBeNull();
});

it("lets taps and scrolls pass through its empty area to the content behind it", () => {
	const tree = render(<Toast toast={{ id: 1, text: "Stopped" }} dismiss={() => {}} />);
	const [root] = tree.root.findAllByType("View" as never);
	expect(root?.props.pointerEvents).toBe("box-none");
});

it("dismisses before running the action, so a follow-up toast the action shows survives the same batch", () => {
	let controller!: ReturnType<typeof useToast>;
	function Harness() {
		controller = useToast();
		return <Toast toast={controller.toast} dismiss={controller.dismiss} />;
	}
	const tree = render(<Harness />);
	act(() =>
		controller.show({
			text: "Session archived",
			action: { label: "Undo", run: () => controller.show({ text: "Restored" }) },
		}),
	);
	act(() => pressable(tree, "Undo")?.props.onPress());
	expect(renderedText(tree)).toContain("Restored");
});

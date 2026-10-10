// The hooks a session screen reads Next's order through: live under an
// AlertsProvider, and harmless on a screen rendered on its own.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { AlertCenter } from "./alertCenter";
import { type Alerts, AlertsContext, useAlertedRecently, useNextUsed } from "./alertsContext";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const timer = {
	now: () => Date.now(),
	setTimeout: (callback: () => void, ms: number) => setTimeout(callback, ms),
	clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};
const probe = { recent: [] as readonly string[], nextUsed: () => {} };
function Probe() {
	probe.recent = useAlertedRecently();
	probe.nextUsed = useNextUsed();
	return null;
}

it("reads who alerted you most recently, and lets Next serve what waits", () => {
	const center = new AlertCenter(timer);
	const alerts: Alerts = { center, reportRoutes: () => {}, noticeFor: () => undefined };
	render(
		<AlertsContext.Provider value={alerts}>
			<Probe />
		</AlertsContext.Provider>,
	);
	const release = center.hold("covered");
	act(() => {
		center.offer({ kind: "question", ref: "a", title: "A", why: null });
		center.offer({ kind: "failed", ref: "b", title: "B", why: null });
	});
	expect(probe.recent).toEqual(["b", "a"]);
	expect(center.getSnapshot().held).toBe(2);
	act(() => probe.nextUsed());
	expect(center.getSnapshot().held).toBe(0);
	release();
});

it("reads nothing and serves nothing outside an AlertsProvider", () => {
	render(<Probe />);
	expect(probe.recent).toEqual([]);
	expect(() => probe.nextUsed()).not.toThrow();
});

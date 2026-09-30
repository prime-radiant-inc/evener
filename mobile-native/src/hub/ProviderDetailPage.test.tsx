// A provider's detail, pushed over the Providers page that publishes it
// (providersScreenSlot.tsx): it shows only the detail its route names, for the
// hub it was opened for, and goes back once that page lets the provider go.
import type { ReactNode } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { type ProviderDetailSlot, ProvidersScreenSlotProvider, usePublishProviderDetail } from "./providersScreenSlot";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", async () => ({
	usePreventRemove: (await import("./backGuardTestUtils")).usePreventRemoveMock,
}));

const navigation = { canGoBack: () => true, goBack: vi.fn(), dispatch: vi.fn() };
beforeEach(() => {
	navigation.goBack.mockClear();
});

const slot = (over: Partial<ProviderDetailSlot>): ProviderDetailSlot => ({
	hubId: "hub-1",
	name: "work",
	detail: "work's detail" as ReactNode,
	guarded: false,
	leave: (then) => then(),
	onGone: () => {},
	...over,
});

function Publisher({ value }: { value: ProviderDetailSlot }) {
	usePublishProviderDetail(value);
	return null;
}

function mount(published: ProviderDetailSlot, route = { hubId: "hub-1", name: "work" }) {
	const page = (value: ProviderDetailSlot, params = route) => (
		<ProvidersScreenSlotProvider>
			<Publisher value={value} />
			<ProviderDetailPage
				navigation={navigation as never}
				route={{ key: "detail", name: "ProviderDetail", params }}
			/>
		</ProvidersScreenSlotProvider>
	);
	const tree = render(page(published));
	return { tree, update: (value: ProviderDetailSlot, params = route) => act(() => tree.update(page(value, params))) };
}

it("shows the detail the Providers page publishes for the provider its route names", () => {
	const { tree } = mount(slot({}));
	expect(renderedText(tree)).toBe("work's detail");
});

// After a hub switch the page underneath belongs to another hub: its detail
// never shows under this route.
it("shows nothing published for another hub", () => {
	const { tree } = mount(slot({ hubId: "hub-2" }));
	expect(renderedText(tree)).toBe("");
});

// While a rename re-targets the route, the selection names the new provider
// first: the page shows neither provider's detail under the old name, and
// waits for the route rather than going back.
it("shows nothing for another provider's name, and waits for the route to catch up", () => {
	const { tree, update } = mount(slot({}));
	update(slot({ name: "home", detail: "home's detail" }));
	expect(renderedText(tree)).toBe("");
	expect(navigation.goBack).not.toHaveBeenCalled();
	update(slot({ name: "home", detail: "home's detail" }), { hubId: "hub-1", name: "home" });
	expect(renderedText(tree)).toBe("home's detail");
});

it("goes back once the Providers page lets its provider go", () => {
	const { update } = mount(slot({}));
	update(slot({ name: null, detail: null }));
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
});

it("hands the selection back when it leaves", () => {
	const onGone = vi.fn();
	const { tree } = mount(slot({ onGone }));
	act(() => tree.unmount());
	expect(onGone).toHaveBeenCalledTimes(1);
});

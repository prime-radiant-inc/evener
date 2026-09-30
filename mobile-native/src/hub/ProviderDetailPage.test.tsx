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

// When the Providers page swaps its detail to another provider (a link opening
// one in place), the route takes the new name at once and the slot follows a
// commit later: the page shows neither detail in between and doesn't go back.
it("shows nothing while the slot catches up with a swapped route, and doesn't go back", () => {
	const { tree, update } = mount(slot({}));
	update(slot({}), { hubId: "hub-1", name: "home" });
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

// Only its own hub's selection: another hub's page sharing the name keeps
// its own (review M5).
it("hands nothing back to another hub's page", () => {
	const onGone = vi.fn();
	const { tree, update } = mount(slot({}));
	update(slot({ hubId: "hub-2", onGone }));
	act(() => tree.unmount());
	expect(onGone).not.toHaveBeenCalled();
});

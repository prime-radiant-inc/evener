// A provider's detail, pushed over the Providers page that publishes it
// (providersScreenSlot.tsx): it shows only the detail its route names, for the
// hub it was opened for, and goes back once that page lets the provider go.
import { type ReactNode, useLayoutEffect } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { ProviderDetailPage } from "./ProviderDetailPage";
import {
	type ProviderDetailSlot,
	ProvidersScreenSlotProvider,
	usePublishProviderDetail,
	useProviderDetailSlotReader,
} from "./providersScreenSlot";

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
			<ProviderDetailPage navigation={navigation as never} route={{ key: "detail", name: "ProviderDetail", params }} />
		</ProvidersScreenSlotProvider>
	);
	const tree = render(page(published));
	return { tree, update: (value: ProviderDetailSlot, params = route) => act(() => tree.update(page(value, params))) };
}

// The page publishes in the commit's layout phase: a layout effect that runs
// after the page's, in the same commit, already finds its publication, so a
// detail pushed in that commit paints with it.
it("publishes before the commit's later layout effects run", () => {
	const seen: (string | null | undefined)[] = [];
	function LayoutReader() {
		const read = useProviderDetailSlotReader();
		useLayoutEffect(() => {
			seen.push(read()?.name);
		}, [read]);
		return null;
	}
	render(
		<ProvidersScreenSlotProvider>
			<Publisher value={slot({})} />
			<LayoutReader />
		</ProvidersScreenSlotProvider>,
	);
	expect(seen).toEqual(["work"]);
});

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

// Nothing swaps the provider under a pushed detail, so a publication for
// another provider means this one has gone: the page shows nothing of either
// and goes back.
it("shows nothing for another provider's name, and goes back", () => {
	const { tree, update } = mount(slot({}));
	update(slot({ name: "home", detail: "home's detail" }));
	expect(renderedText(tree)).toBe("");
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
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

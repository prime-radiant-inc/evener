// The slot the Providers page publishes its detail into
// (providersScreenSlot.tsx): a publication lands in the commit's layout
// phase, before paint.
import { type ReactNode, useLayoutEffect } from "react";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import {
	type ProviderDetailSlot,
	ProvidersScreenSlotProvider,
	usePublishProviderDetail,
	useProviderDetailSlotReader,
} from "./providersScreenSlot";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const slot: ProviderDetailSlot = {
	hubId: "hub-1",
	name: "work",
	detail: "work's detail" as ReactNode,
	guarded: false,
	leave: (then) => then(),
	onGone: () => {},
};

function Publisher() {
	usePublishProviderDetail(slot);
	return null;
}

// A layout effect that runs after the page's, in the same commit, already
// finds its publication: so a detail pushed in that commit paints with it.
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
			<Publisher />
			<LayoutReader />
		</ProvidersScreenSlotProvider>,
	);
	expect(seen).toEqual(["work"]);
});

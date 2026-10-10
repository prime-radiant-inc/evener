// The generic Hub screen slot (hubScreenSlot.tsx): one publish/read mechanism
// for the pages a list page pushes over itself. These pin what both the
// Plugins marketplace page and the Providers detail rely on: a publication
// lands in the commit that made it, reaches only its own kind, and is taken
// back when the owning page goes.
import { useLayoutEffect } from "react";
import { act } from "react-test-renderer";
import { expect, it } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import {
	createHubScreenSlot,
	type PluginsScreenSlot,
	PluginsScreenSlotProvider,
	usePluginsScreenSlot,
	usePublishPluginsScreen,
} from "./hubScreenSlot";

// A publication happens in the owning page's layout effect, so a reader's later
// layout effect, in the same commit, already finds it: a page pushed in the
// commit that builds it paints with its content.
it("publishes into the slot before the commit's later layout effects run", () => {
	const seen: (string | undefined)[] = [];
	const strings = createHubScreenSlot<string>();
	function Publisher() {
		strings.usePublish("first");
		return null;
	}
	function LaterLayoutReader() {
		const read = strings.useSlotReader();
		useLayoutEffect(() => {
			seen.push(read() ?? undefined);
		}, [read]);
		return null;
	}
	render(
		<strings.Provider>
			<Publisher />
			<LaterLayoutReader />
		</strings.Provider>,
	);
	expect(seen).toEqual(["first"]);
});

// Each kind gets its own store, so the two slots sharing one mechanism can't
// read each other's payload.
it("keeps each slot's publication to its own kind", () => {
	const seen: (string | number | null)[] = [];
	const strings = createHubScreenSlot<string>();
	const numbers = createHubScreenSlot<number>();
	function Publisher() {
		strings.usePublish("published");
		return null;
	}
	function Reader() {
		seen.push(strings.useSlot(), numbers.useSlot());
		return null;
	}
	render(
		<strings.Provider>
			<numbers.Provider>
				<Publisher />
				<Reader />
			</numbers.Provider>
		</strings.Provider>,
	);
	expect(seen).toContain("published");
	expect(seen.some((value) => typeof value === "number")).toBe(false);
});

it("takes a publication back when the publishing page goes", () => {
	const seen: (string | null)[] = [];
	const strings = createHubScreenSlot<string>();
	function Publisher() {
		strings.usePublish("held");
		return null;
	}
	function Reader() {
		seen.push(strings.useSlot());
		return null;
	}
	const tree = render(
		<strings.Provider>
			<Publisher />
			<Reader />
		</strings.Provider>,
	);
	expect(seen.at(-1)).toBe("held");
	act(() =>
		tree.update(
			<strings.Provider>
				<Reader />
			</strings.Provider>,
		),
	);
	expect(seen.at(-1)).toBeNull();
});

// The Plugins page's own slot is the generic mechanism, so its publication must
// reach the marketplace page pushed over it.
it("serves the Plugins page's publication through the same slot", () => {
	const published = {} as PluginsScreenSlot;
	function Publisher() {
		usePublishPluginsScreen(published);
		return null;
	}
	function Marketplace() {
		return usePluginsScreenSlot() ? "shown" : "gone";
	}
	const tree = render(
		<PluginsScreenSlotProvider>
			<Publisher />
			<Marketplace />
		</PluginsScreenSlotProvider>,
	);
	expect(renderedText(tree)).toBe("shown");
});

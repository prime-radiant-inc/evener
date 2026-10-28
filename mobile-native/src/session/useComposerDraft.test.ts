import { nativePlatform } from "../conversationPlatform.testkit";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { drafts } from "../nativeDrafts";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";
import { useComposerDraft } from "./useComposerDraft";

afterAll(() => {
	for (const database of nativePlatform.sqliteDatabases) database.close();
});
afterEach(() => {
	unmountMountedTrees();
	vi.restoreAllMocks();
});

const target = { hubId: "hub-leave", sessionRef: "ref-leave" };

it("retries pending handoffs once the draft is loaded and writable", () => {
	const retry = vi.spyOn(drafts, "retryPendingHandoffs");
	const abortOnLeave = { current: null };
	const hook = renderHook(() => useComposerDraft({ ...target, focused: true, abortOnLeave }));
	expect(hook.result.current.draft.loaded).toBe(true);
	expect(retry).toHaveBeenCalled();
});

it("cancels the staged image selection and aborts the lookup when the screen leaves the front", () => {
	const lookup = new AbortController();
	const abortOnLeave = { current: lookup };
	let focused = true;
	const hook = renderHook(() => useComposerDraft({ ...target, focused, abortOnLeave }));
	const cancel = vi.spyOn(hook.result.current.imageSelection, "cancel");
	expect(lookup.signal.aborted).toBe(false);
	focused = false;
	hook.rerender();
	expect(cancel).toHaveBeenCalledTimes(1);
	expect(lookup.signal.aborted).toBe(true);
});

it("keeps a photo being attached while the screen stays in front", () => {
	const lookup = new AbortController();
	const abortOnLeave = { current: lookup };
	const hook = renderHook(() => useComposerDraft({ ...target, focused: true, abortOnLeave }));
	const cancel = vi.spyOn(hook.result.current.imageSelection, "cancel");
	hook.rerender();
	expect(cancel).not.toHaveBeenCalled();
	expect(lookup.signal.aborted).toBe(false);
});

import { expect, it, vi } from "vitest";
import { ComposerFocus } from "./composerFocus";

it("starts unfocused and tells its readers only when focus changes", () => {
	const focus = new ComposerFocus();
	const listener = vi.fn();
	const unsubscribe = focus.subscribe(listener);
	expect(focus.getSnapshot()).toBe(false);
	focus.set(false);
	expect(listener).not.toHaveBeenCalled();
	focus.set(true);
	expect(focus.getSnapshot()).toBe(true);
	expect(listener).toHaveBeenCalledTimes(1);
	focus.set(true);
	expect(listener).toHaveBeenCalledTimes(1);
	unsubscribe();
	focus.set(false);
	expect(focus.getSnapshot()).toBe(false);
	expect(listener).toHaveBeenCalledTimes(1);
});

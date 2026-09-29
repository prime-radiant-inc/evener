// Whether the Session's composer field has focus: the Composer reports it,
// and what steps aside while you type in it reads it (useComposerTyping).
// It's held outside the screen's state so that focus coming and going
// re-renders only those readers, never the screen and its transcript (#3247).
export class ComposerFocus {
	#focused = false;
	readonly #listeners = new Set<() => void>();

	getSnapshot = (): boolean => this.#focused;

	subscribe = (listener: () => void): (() => void) => {
		this.#listeners.add(listener);
		return () => this.#listeners.delete(listener);
	};

	set(focused: boolean): void {
		if (focused === this.#focused) return;
		this.#focused = focused;
		for (const listener of this.#listeners) listener();
	}
}

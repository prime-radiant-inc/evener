// Which control the composer's trailing slot shows right now (spec 8.5):
// Stop while the agent works and there is nothing to send, Send otherwise.
//
// The screen decides this, not the composer: only the screen sees the whole
// draft, and an image-only draft (a photo attached, its text marker deleted)
// is a message Send must still carry.
export interface SlotState {
	/** The hub is reachable: Stop needs the hub to act. */
	connected: boolean;
	/** The running turn can be stopped (`conversationControls(...).stop`). */
	canStop: boolean;
	/** The typed text. */
	text: string;
	/** How many images the draft holds. */
	images: number;
}

export function composerShowsStop({ connected, canStop, text, images }: SlotState): boolean {
	return connected && canStop && text.trim() === "" && images === 0;
}

/** What the composer needs to know to rest one line tall: the whole draft (text
 * and images) and whether the field can take focus. The screen owns this since
 * only it sees the draft; the composer adds its own focus bit. */
export interface RestState {
	/** The typed text. */
	text: string;
	/** How many images the draft holds. */
	images: number;
	/** Whether the field can take focus. A failed draft read leaves it false, and
	 * focus is the only way back to the controls row, so it must not rest. */
	editable: boolean;
}

/** Whether the composer may rest one line tall (Jesse). Only a truly empty
 * draft with a focusable field rests: an attached image is a message, and
 * whitespace is a draft the field visibly holds. */
export function composerRests({ text, images, editable }: RestState): boolean {
	return text === "" && images === 0 && editable;
}

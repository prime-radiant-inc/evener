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

// Asks the Board to scroll to a section. The Board's route takes no params
// (restoring the app depends on that), so a coalesced banner's "3 sessions
// need you" reaches Needs you through here. A request made while no Board
// listens waits for the first one.
export type BoardSection = "needsYou";
type Listener = (section: BoardSection) => void;

const listeners = new Set<Listener>();
let waiting: BoardSection | null = null;

export function requestBoardJump(section: BoardSection): void {
	if (listeners.size === 0) {
		waiting = section;
		return;
	}
	for (const listener of [...listeners]) listener(section);
}

export function onBoardJump(listener: Listener): () => void {
	listeners.add(listener);
	if (waiting !== null) {
		const section = waiting;
		waiting = null;
		listener(section);
	}
	return () => {
		listeners.delete(listener);
	};
}

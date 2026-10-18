// Fixtures for the share suites: a payload, a file name, and an in-memory
// directory that answers as the app group's does.
import type { SharePort, SharedPayload } from "./sharePayload";
import { shareFileName } from "./sharePayload";

const HOUR = 60 * 60 * 1000;

export function share(id: string, at: number): SharedPayload {
	return { id, text: `text ${id}`, createdAt: new Date(at).toISOString() };
}

/** A directory of share files that answers immediately. */
export function sharePort(shares: SharedPayload[]): SharePort {
	const files = new Map(shares.map((entry) => [shareFileName(entry.id), entry]));
	return {
		async list() {
			return [...files.keys()];
		},
		async read(name) {
			const entry = files.get(name);
			if (!entry) throw new Error("gone");
			return JSON.stringify(entry);
		},
		async remove(name) {
			files.delete(name);
		},
	};
}

export { HOUR };

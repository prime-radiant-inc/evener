import { restoreUnconfirmedDraft } from "./draftRecovery";
import type {
	DraftDestination,
	DraftRecord,
	DraftRepository,
} from "./draftRepository";

interface DraftSnapshot {
	record: DraftRecord;
	loaded: boolean;
	submitting: boolean;
	error: string | null;
}

/** Composition survives the lifetime of a socket and a conversation store. */
export class DraftDocument {
	private snapshot: DraftSnapshot = {
		record: { draft: "", unconfirmed: null },
		loaded: false,
		submitting: false,
		error: null,
	};
	private listeners = new Set<() => void>();
	private forgotten = false;

	constructor(
		private repository: () => Pick<DraftRepository, "read" | "write">,
		private destination: DraftDestination,
	) {
		this.retry();
	}

	getSnapshot = (): DraftSnapshot => this.snapshot;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private update(update: Partial<DraftSnapshot>) {
		this.snapshot = { ...this.snapshot, ...update };
		for (const listener of this.listeners) listener();
	}
	private persist(record: DraftRecord, retainOnFailure: boolean) {
		if (this.forgotten) return;
		try {
			this.repository().write(this.destination, record);
			this.update({ record, error: null });
		} catch (error) {
			this.update({
				...(retainOnFailure ? { record } : {}),
				error:
					"Your latest draft could not be saved on this device. Keep this screen open and retry saving.",
			});
			throw error;
		}
	}
	retry = () => {
		if (this.forgotten) return;
		try {
			if (this.snapshot.loaded) this.persist(this.snapshot.record, true);
			else
				this.update({
					record: this.repository().read(this.destination),
					loaded: true,
					error: null,
				});
		} catch {
			if (!this.snapshot.loaded)
				this.update({
					error:
						"Your saved draft could not be loaded. Retry before composing.",
				});
		}
	};
	forget() {
		this.forgotten = true;
		this.update({
			record: { draft: "", unconfirmed: null },
			loaded: false,
			error: null,
		});
	}
	edit(text: string) {
		if (!this.snapshot.loaded) return;
		try {
			this.persist({ ...this.snapshot.record, draft: text }, true);
		} catch {
			/* The snapshot retains unsaved input and its error. */
		}
	}
	async submit(operation: (text: string) => Promise<boolean>) {
		return this.submitContent(this.snapshot.record.draft, false, operation);
	}
	async submitText(
		text: string,
		operation: (text: string) => Promise<boolean>,
	) {
		return this.submitContent(text, true, operation);
	}
	private async submitContent(
		text: string,
		preserveDraft: boolean,
		operation: (text: string) => Promise<boolean>,
	) {
		const { record, loaded, submitting, error } = this.snapshot;
		if (
			!loaded ||
			submitting ||
			error ||
			record.unconfirmed !== null ||
			!text.trim()
		)
			return;
		// Persist before invoking any transport operation. A crash after this point
		// can only establish uncertainty, never that it is safe to replay the input.
		this.persist(
			{ draft: preserveDraft ? record.draft : "", unconfirmed: text },
			false,
		);
		this.update({ submitting: true });
		try {
			if (await operation(text))
				this.persist({ ...this.snapshot.record, unconfirmed: null }, true);
		} finally {
			this.update({ submitting: false });
		}
	}
	dismiss() {
		if (!this.snapshot.loaded || this.snapshot.submitting) return;
		try {
			this.persist({ ...this.snapshot.record, unconfirmed: null }, true);
		} catch {
			/* Retry retains the requested recovery state. */
		}
	}
	restore() {
		const { record, loaded, submitting } = this.snapshot;
		if (!loaded || submitting || record.unconfirmed === null) return;
		const text = restoreUnconfirmedDraft(record.draft, record.unconfirmed);
		if (text === null) return;
		try {
			this.persist({ draft: text, unconfirmed: null }, true);
		} catch {
			/* Unsaved recovered text stays visible. */
		}
	}
}

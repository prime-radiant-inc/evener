import type {
	HubInput,
	HubProfile,
	HubProfiles,
	HubUpdate,
} from "./connection";
import { removeSavedHub } from "./removeHub";

export interface HubSelectionCallbacks {
	onProfiles(profiles: HubProfile[]): void;
	onSelect(id: string | null): void;
	onRetry(): void;
}

export class HubSelection {
	private currentId: string | null = null;
	private intentRevision = 0;
	private rosterRevision = 0;
	private roster: HubProfile[] = [];

	constructor(
		private profiles: HubProfiles,
		private callbacks: HubSelectionCallbacks,
		private createId: () => string,
	) {}

	private publish(profiles: HubProfile[]): void {
		this.roster = profiles;
		this.callbacks.onProfiles(profiles);
	}

	async load(): Promise<HubProfile[]> {
		const revision = ++this.rosterRevision;
		const profiles = await this.profiles.list();
		// Storage reads can overlap; only the newest read owns the visible roster.
		if (revision === this.rosterRevision) this.publish(profiles);
		return profiles;
	}

	select(id: string): void {
		this.intentRevision += 1;
		this.currentId = id;
		this.callbacks.onSelect(id);
		this.callbacks.onRetry();
	}

	disconnect(): void {
		this.intentRevision += 1;
		this.currentId = null;
		this.callbacks.onSelect(null);
	}

	restore(id: string | null): void {
		if (this.intentRevision !== 0 || this.currentId !== null) return;
		if (id !== null) {
			this.currentId = id;
			this.callbacks.onSelect(id);
		}
	}

	async save(input: HubInput): Promise<boolean> {
		const revision = ++this.intentRevision;
		const id = this.createId();
		const profile = await this.profiles.save({ ...input, id });
		await this.load();
		if (revision !== this.intentRevision) return false;
		this.currentId = profile.id;
		this.callbacks.onSelect(profile.id);
		return true;
	}

	async update(id: string, input: HubUpdate): Promise<void> {
		await this.profiles.update(id, input);
		try {
			await this.load();
		} finally {
			// The credentials were saved even if refreshing the roster fails.
			if (input.token !== undefined && this.currentId === id)
				this.callbacks.onRetry();
		}
	}

	async remove(
		id: string,
		drafts: Parameters<typeof removeSavedHub>[1],
	): Promise<void> {
		this.intentRevision += 1;
		let readRevision = 0;
		const result = await removeSavedHub(
			{
				remove: (hubId) => this.profiles.remove(hubId),
				list: () => {
					readRevision = this.rosterRevision + 1;
					return this.load();
				},
			},
			drafts,
			id,
		);
		if (
			result.removed &&
			!result.profiles &&
			readRevision === this.rosterRevision
		)
			this.publish(this.roster.filter((profile) => profile.id !== id));
		if (result.removed && this.currentId === id) {
			this.currentId = null;
			this.callbacks.onSelect(null);
		}
		if (result.error) throw new Error(result.error);
	}
}

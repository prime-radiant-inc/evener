// The session's shared notes and links (spec 8.8): what the notes bar
// previews, what the editor's status line says, and the controller that
// saves your note and removes links.
//
// Saving is a direct notes/human/set: the durable runtime carries only the
// four turn kinds (ruling 32). So a note that hasn't reached the hub is kept
// on this phone, under evener.native.note-draft.<hubId>, until it does
// (Review Focus 5).
import type { NotesHumanSetResponse, ThreadModel } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { SyncStringStorage } from "../syncStringStorage";

/** The daemon clamps a note to 1,000 runes (agent/session_notes.go:29); the
 * editor stops there so nothing is clipped silently. */
export const NOTE_LIMIT = 1000;
/** Leaving the field saves ten seconds later, as the web does
 * (cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts). */
export const SAVE_AFTER_BLUR_MS = 10_000;
/** How long after the last keystroke the draft reaches the kv-store. The
 * store parses and stringifies the whole per-hub drafts map on every write, so
 * one write per burst keeps typing smooth; a crash inside this window loses at
 * most the last few keystrokes (they stay in memory until then). Shorter than
 * the save-after-blur wait, and flushed at once when you leave the field, the
 * app backgrounds or the sheet closes. */
export const DRAFT_WRITE_DEBOUNCE_MS = 500;

export type NotesGlyph = "person" | "sparkles" | "link";

export interface NotesBarPreview {
	glyph: NotesGlyph;
	text: string;
	/** Trailing "3 links" when a note shows and links exist: a bare glyph and
	 * count read as attachments (spec 8.8). */
	links?: string;
}

const oneLine = (text: string) => text.replace(/\s+/g, " ").trim();
const linkCount = (count: number) => `${count} ${count === 1 ? "link" : "links"}`;

export function notesBarPreview(
	session: Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls" | "capabilities">,
): NotesBarPreview | null {
	// The package's canReadSharedNotes rule (sharedNotesAvailability.ts).
	if (session.capabilities.sharedNotes !== true) return null;
	const count = session.sessionUrls.length;
	const links = count > 0 ? linkCount(count) : undefined;
	const human = oneLine(session.humanNote);
	if (human) return { glyph: "person", text: `Your note: ${human}`, links };
	const agent = oneLine(session.agentNote);
	if (agent) return { glyph: "sparkles", text: `Agent's note: ${agent}`, links };
	const only = count === 1 ? session.sessionUrls[0] : undefined;
	if (only) return { glyph: "link", text: only.label?.trim() || only.url };
	if (count > 1) return { glyph: "link", text: linkCount(count) };
	return null;
}

/** Whether this phone may change your note: the web's canWriteHumanNote
 * (cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts). */
export function canWriteHumanNote(session: Pick<ThreadModel, "status" | "resumeRequired" | "capabilities">): boolean {
	return (
		session.capabilities.sharedNotes === true &&
		session.resumeRequired !== true &&
		!["ended", "closed", "notLoaded", "restartRequired"].includes(session.status.type)
	);
}

export type NotePhase = "clean" | "editing" | "scheduled" | "saving" | "saved" | "failed";

export function noteStatusLine(phase: NotePhase, working: boolean): string {
	if (phase === "scheduled") return "Saves in 10 seconds, or when you close this.";
	if (phase === "saving") return "Saving…";
	if (phase === "saved") return "Saved";
	if (phase === "failed") return "Couldn't save your note yet. It's kept on this phone.";
	return working
		? "Your note stays on this session. The agent is told when it changes."
		: "Your note stays on this session. Saving it will wake the agent.";
}

const draftKey = (hubId: string) => `evener.native.note-draft.${hubId}`;

function readDrafts(storage: SyncStringStorage, hubId: string): Record<string, string> {
	try {
		const value: unknown = JSON.parse(storage.getItemSync(draftKey(hubId)) ?? "{}");
		if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
		return Object.fromEntries(
			Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
		);
	} catch {
		return {};
	}
}

function writeDrafts(storage: SyncStringStorage, hubId: string, drafts: Record<string, string>): void {
	try {
		if (Object.keys(drafts).length > 0) storage.setItemSync(draftKey(hubId), JSON.stringify(drafts));
		else storage.removeItemSync(draftKey(hubId));
	} catch {
		// The in-memory text still holds it for this launch.
	}
}

export function forgetNoteDrafts(storage: SyncStringStorage, hubId: string): void {
	writeDrafts(storage, hubId, {});
}

export interface NotesControllerOptions {
	client: Pick<ConversationClientLike, "request">;
	hubId: string;
	ref: string;
	/** The live session's instance id, read at each request. */
	instanceId(): string | undefined;
	/** Your note as the hub last reported it. */
	savedNote(): string;
	/** Whether a turn is running, read at each save. */
	working(): boolean;
	/** Whether the session takes notes now (canWriteHumanNote), read at each
	 * save. While it doesn't, nothing is sent and the note stays on this
	 * phone. */
	writable(): boolean;
	storage: SyncStringStorage;
	uuid(): string;
}

export interface SaveOutcome {
	saved: boolean;
	/** The save woke an agent that wasn't in a turn: the toast says the agent is reading it. */
	woke: boolean;
}

interface NoteState {
	text: string;
	phase: NotePhase;
}

export class NotesController {
	private state: NoteState;
	private timer: ReturnType<typeof setTimeout> | null = null;
	private draftTimer: ReturnType<typeof setTimeout> | null = null;
	private draftValue: string | undefined;
	/** The notes this flush's save chain has sent, newest last. A hub value
	 * among them is this chain's own echo (even a late, out-of-order broadcast),
	 * not a third writer. Scoped to the chain, so a remote write that restores
	 * an older value is not mistaken for one, and kept whole for the chain's
	 * life so an early echo is never evicted (RoboRev #2769 round 5, round 6). */
	private chainSent: string[] = [];
	private saving: Promise<SaveOutcome> | null = null;
	private listeners = new Set<() => void>();

	constructor(private readonly options: NotesControllerOptions) {
		const kept = readDrafts(options.storage, options.hubId)[options.ref];
		this.state = kept !== undefined ? { text: kept, phase: "failed" } : { text: options.savedNote(), phase: "clean" };
	}

	getSnapshot = (): NoteState => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	/** The hub's note changed (evener/notes/updated). Follow it, unless there
	 * is text here the hub hasn't confirmed: that stays yours. */
	sync(): void {
		const hub = this.options.savedNote();
		// The hub caught up to text you're still editing, or that a blur has
		// scheduled to save: it needs no draft and nothing to send, so read
		// clean rather than leaving one behind (RoboRev #3160).
		if ((this.state.phase === "editing" || this.state.phase === "scheduled") && hub === this.state.text) {
			this.clearDraft();
			this.publish({ text: hub, phase: "clean" });
			return;
		}
		if (this.state.phase !== "clean" && this.state.phase !== "saved") return;
		// A note this controller didn't save itself makes "Saved" stale, so
		// adopting it reads clean.
		if (hub !== this.state.text) this.publish({ text: hub, phase: "clean" });
	}

	edit(text: string): void {
		this.cancelTimer();
		// By code point, not UTF-16 unit: the daemon clamps by runes
		// (agent/session_notes.go:29), and text.slice(0, NOTE_LIMIT) counts
		// UTF-16 units, which can split an astral character's surrogate pair
		// or cut well short of 1,000 runes for one made entirely of them
		// (RoboRev #2769 round 2).
		const clipped = Array.from(text).slice(0, NOTE_LIMIT).join("");
		// Typed back to exactly the hub's current text: nothing to keep or send,
		// and going clean lets sync() resume following the hub. Otherwise a
		// later hub update would sit unseen behind this stale "editing" text,
		// and a blur's save would overwrite it (RoboRev #2769).
		if (clipped === this.options.savedNote()) {
			this.clearDraft();
			this.publish({ text: clipped, phase: "clean" });
			return;
		}
		this.scheduleDraft(clipped);
		this.publish({ text: clipped, phase: "editing" });
	}

	/** Returning to the field cancels a scheduled save, as the web does. */
	focus(): void {
		if (this.state.phase !== "scheduled") return;
		this.cancelTimer();
		this.publish({ ...this.state, phase: "editing" });
	}

	blur(): void {
		// Leaving the field flushes the draft now, before the debounce would.
		this.writeDraft();
		if (!this.unsaved()) {
			// The hub already holds this text: keep nothing on the phone.
			this.clearDraft();
			return;
		}
		this.cancelTimer();
		this.timer = setTimeout(() => {
			this.timer = null;
			void this.flush();
		}, SAVE_AFTER_BLUR_MS);
		this.publish({ ...this.state, phase: "scheduled" });
	}

	/** Save now, if anything is unsaved. Closing the sheet, backgrounding the
	 * app and opening the session connected all call this. */
	flush(): Promise<SaveOutcome> {
		this.cancelTimer();
		// Backgrounding the app and closing the sheet both land here: the draft
		// reaches the kv-store before the save (or the return) does.
		this.writeDraft();
		const nothing: SaveOutcome = { saved: false, woke: false };
		if (!this.unsaved()) {
			// The hub caught up: nothing to save, and nothing to keep on the phone
			// (the write above may have just put this text there).
			this.clearDraft();
			// Nothing left to send and no save armed any more: read clean rather
			// than leaving the editor visibly editing, or promising a save seconds
			// away, with nothing left to save (RoboRev #3160). Only the phases that
			// read as unsaved here: a just-confirmed "saved" stands, and a save in
			// flight owns its own settle.
			if (this.state.phase === "editing" || this.state.phase === "scheduled")
				this.publish({ ...this.state, phase: "clean" });
			return Promise.resolve(nothing);
		}
		if (!this.options.writable()) {
			// The note stays on this phone; with the timer gone, no save is
			// scheduled any more, so the status line must stop promising one.
			if (this.state.phase === "scheduled") this.publish({ ...this.state, phase: "editing" });
			return Promise.resolve(nothing);
		}
		this.saving ??= this.saveUntilClean().finally(() => {
			this.saving = null;
		});
		return this.saving;
	}

	/** Keeps saving while text typed during a save leaves something new
	 * unsaved, so a caller's outcome is never "saved" while newer text is
	 * still sitting there unsent (RoboRev #2769). Stops the moment a save
	 * itself fails, rather than retrying in a tight loop. */
	private async saveUntilClean(): Promise<SaveOutcome> {
		// A new chain: only echoes of its own saves are suppressed (RoboRev #2769).
		this.chainSent = [];
		let outcome = await this.save();
		// save() itself tells "typed on during the save" apart from "settled"
		// by the phase it publishes (see below); unsaved() can't: savedNote()
		// only catches up once the hub's own notes/updated notification
		// arrives, which would make this loop forever on a settled save.
		// Each chained save checks again that the session still takes notes;
		// otherwise the newer text stays on this phone, kept as a draft.
		while (outcome.saved && this.state.phase === "editing" && this.options.writable()) outcome = await this.save();
		return outcome;
	}

	async removeLink(id: string): Promise<boolean> {
		const instanceId = this.options.instanceId();
		if (!instanceId) return false;
		try {
			await this.options.client.request("urls/remove", {
				ref: this.options.ref,
				clientMutationId: this.options.uuid(),
				expectedInstanceId: instanceId,
				id,
			});
			return true;
		} catch (error) {
			// Already gone counts as removed, as on the web (NotesPanel.tsx).
			return error instanceof Error && error.message.includes("no URL entry with id");
		}
	}

	dispose(): void {
		this.cancelTimer();
		// A draft still inside its debounce window would otherwise be lost on
		// teardown; edit() used to write it synchronously, so keep that promise.
		this.writeDraft();
		this.listeners.clear();
	}

	private unsaved(): boolean {
		if (this.state.phase === "failed") return true;
		// A locally confirmed save is trusted on its own: savedNote() only
		// catches up once the hub's own evener/notes/updated notification
		// arrives, which can lag well behind our own "saved" publish, and
		// comparing against it here would resend the same note again in that
		// window (RoboRev #2769 round 3).
		if (this.state.phase === "clean" || this.state.phase === "saved") return false;
		return this.state.text !== this.options.savedNote();
	}

	private async save(): Promise<SaveOutcome> {
		const text = this.state.text;
		const instanceId = this.options.instanceId();
		if (!instanceId) {
			// "failed" always means a draft is on disk backing it, or a crash
			// before the next flush would silently forget it on relaunch - even
			// when, as here, edit() already went clean and back to "failed" in
			// between (RoboRev #2769 round 2).
			this.storeDraft(this.state.text);
			this.publish({ ...this.state, phase: "failed" });
			return { saved: false, woke: false };
		}
		const working = this.options.working();
		// The hub's note as this save begins. A different value at settle time,
		// that is neither this nor what we sent, is a third writer's newer note.
		const startedHubNote = this.options.savedNote();
		this.publish({ ...this.state, phase: "saving" });
		try {
			const response: NotesHumanSetResponse = await this.options.client.request("notes/human/set", {
				ref: this.options.ref,
				clientMutationId: this.options.uuid(),
				expectedInstanceId: instanceId,
				note: text,
			});
			// Remember every note this chain sends: an echo of any of them arriving
			// mid-flight (even a late one from an earlier save in the chain) is ours.
			this.chainSent.push(response.note);
			if (this.state.text === text) {
				this.clearDraft();
				const hub = this.options.savedNote();
				// Someone else (the web, another device) wrote while our set was
				// in flight: showing our text as Saved would hide their newer note
				// and our next edit would overwrite it, so adopt theirs as clean
				// (RoboRev #2769).
				if (hub !== startedHubNote && hub !== response.note && !this.chainSent.includes(hub))
					this.publish({ text: hub, phase: "clean" });
				else this.publish({ text: response.note, phase: "saved" });
			} else {
				// Typed on during the save: the newer text stays kept and unsaved,
				// unless it went back to the hub's note, which leaves nothing to send.
				const caughtUp = this.state.text === this.options.savedNote();
				if (caughtUp) this.clearDraft();
				this.publish({ ...this.state, phase: caughtUp ? "clean" : "editing" });
			}
			// An unchanged note projects "removed" and wakes no one
			// (agent/session_notes_rpc.go).
			return { saved: true, woke: !working && response.receipt.projectionState === "pending" };
		} catch {
			this.storeDraft(this.state.text);
			this.publish({ ...this.state, phase: "failed" });
			return { saved: false, woke: false };
		}
	}

	/** Arm the debounced draft write, restarting the window on every keystroke.
	 * The text itself is already in `state.text`; only the kv-store write waits. */
	private scheduleDraft(text: string): void {
		this.draftValue = text;
		this.cancelDraftTimer();
		this.draftTimer = setTimeout(() => {
			this.draftTimer = null;
			this.writeDraft();
		}, DRAFT_WRITE_DEBOUNCE_MS);
	}

	/** Write the draft now, if a debounced one is waiting. */
	private writeDraft(): void {
		this.cancelDraftTimer();
		if (this.draftValue === undefined) return;
		const text = this.draftValue;
		this.draftValue = undefined;
		// A value the hub already holds needs no draft: keeping one would start
		// the next launch "failed" on it and resend stale text (RoboRev #2769).
		this.storeDraft(text === this.options.savedNote() ? undefined : text);
	}

	/** Drop the draft at once: the text went back to the hub's own, or a save
	 * just landed, so a waiting debounced write must not re-add it. */
	private clearDraft(): void {
		this.cancelDraftTimer();
		this.draftValue = undefined;
		this.storeDraft(undefined);
	}

	/** Keep `text` on the phone, or forget it (`undefined`) once it's saved.
	 * Reads the hub's whole drafts map fresh each time: two sessions open
	 * under one hub each keep their own controller, and a cached copy here
	 * would let the second writer's save erase the first's draft. */
	private storeDraft(text: string | undefined): void {
		const drafts = readDrafts(this.options.storage, this.options.hubId);
		if (text === undefined) delete drafts[this.options.ref];
		else drafts[this.options.ref] = text;
		writeDrafts(this.options.storage, this.options.hubId, drafts);
	}

	private cancelTimer(): void {
		if (this.timer !== null) clearTimeout(this.timer);
		this.timer = null;
	}

	private cancelDraftTimer(): void {
		if (this.draftTimer !== null) clearTimeout(this.draftTimer);
		this.draftTimer = null;
	}

	private publish(state: NoteState): void {
		this.state = state;
		for (const listener of [...this.listeners]) listener();
	}
}

import type { SandboxEscalationRequested } from "@evener/appwire-client";
import type { ConversationClientLike } from "./conversation/services/conversation";

// The dock re-reads the session on its own after this, so it names nothing
// to press. A new decision may still be tried while it stands: the hub refuses
// a resolve for an escalation it already settled (agent/session_escalation.go's
// ResolveSandboxEscalation), so trying again never decides twice.
const UNCONFIRMED = "Couldn't confirm your decision. It may already have been applied.";

/** Decisions belong to the displayed approval and one live session binding. */
export class ApprovalControls {
	private state = {
		pending: null as string | null,
		refreshing: false,
		error: null as string | null,
	};
	private disposed = false;
	private listeners = new Set<() => void>();
	constructor(
		private client: ConversationClientLike,
		private ref: string,
		private approvals: () => SandboxEscalationRequested[],
		private current: () => boolean,
		private refreshSession: () => Promise<void>,
	) {}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	dispose() {
		this.disposed = true;
		this.listeners.clear();
	}
	private publish(state: typeof this.state) {
		this.state = state;
		for (const listener of this.listeners) listener();
	}
	/** Reads the session again. `acknowledged` says the hub already took the
	 * decision this read follows, so the read is catch-up: its failure leaves
	 * the acknowledgement standing instead of reporting the decision as
	 * unconfirmed. A read nobody's acknowledgement stands behind keeps the
	 * uncertainty, because nothing else says what the hub did. */
	private async refreshAuthoritative(acknowledged = false) {
		if (this.disposed || !this.current()) return;
		this.publish({ ...this.state, refreshing: true });
		try {
			await this.refreshSession();
			if (this.disposed) return;
			if (!this.current()) throw new Error("Session changed");
			this.publish({ pending: null, refreshing: false, error: null });
		} catch {
			if (this.disposed) return;
			// A covered pane's decision still settled at the hub; its readback
			// failing is not a reason to offer Allow and Deny again.
			this.publish({
				pending: null,
				refreshing: false,
				error: acknowledged ? null : UNCONFIRMED,
			});
		}
	}
	async refresh() {
		if (this.disposed || !this.current() || this.state.pending !== null || this.state.refreshing) return;
		await this.refreshAuthoritative();
	}
	/** Makes the decision at the hub. True when the hub's receipt took it,
	 * whatever the reader is looking at now and whatever the catch-up read
	 * managed afterwards; whether to announce it is the screen's question,
	 * since the pane that asked may have been covered by then. False means the
	 * hub refused it, or nothing came back to say what it did. */
	async resolve(displayed: SandboxEscalationRequested, approve: boolean): Promise<boolean> {
		if (this.disposed || !this.current() || this.state.pending || this.state.refreshing) return false;
		const pending = this.approvals().find((value) => value.escalationId === displayed.escalationId);
		if (!pending || JSON.stringify(pending) !== JSON.stringify(displayed)) return false;
		this.publish({ pending: displayed.escalationId, refreshing: false, error: null });
		try {
			const receipt = await this.client.request("evener/sandbox/escalation/resolve", {
				ref: this.ref,
				escalationId: displayed.escalationId,
				approve,
			});
			if (!receipt || typeof receipt !== "object" || Array.isArray(receipt))
				throw new Error("Malformed resolution receipt");
			if (this.disposed) return false;
			// The receipt is the hub's acknowledgement, and it is what settles this
			// decision. The read that follows is catch-up: it brings the session's
			// new state on screen, and a read that fails leaves the acknowledgement
			// standing rather than turning an accepted decision into an unconfirmed
			// one the reader is offered again.
			if (!this.current()) throw new Error("Session changed");
			await this.refreshAuthoritative(true);
			return true;
		} catch {
			if (this.disposed) return false;
			this.publish({
				pending: null,
				refreshing: false,
				error: UNCONFIRMED,
			});
			return false;
		}
	}
}

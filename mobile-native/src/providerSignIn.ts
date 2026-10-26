import type { AuthDeviceStartResponse, AuthLoginStartResponse, AuthStatusResponse } from "@evener/appwire-client";
import type { CredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import { isStaleListingRefusal } from "@evener/appwire-client/state/credentials";

/** What went wrong in a sign-in, or what it couldn't confirm. */
export type SignInProblem =
	| "connectionChanged"
	| "startFailed"
	| "statusUnconfirmed"
	| "notConfirmed"
	| "checkFailed"
	| "redirectMissing"
	| "completionUnconfirmed"
	| "statusCheckFailed";

// What happened and the one thing to do (spec 5). The sheet's title names the
// provider, and its status line says when the connection itself is down.
const SIGN_IN_PROBLEMS: Record<SignInProblem, string> = {
	connectionChanged:
		"Sign-in could not be confirmed before the connection changed. See whether Providers shows it signed in before trying again.",
	startFailed: "The hub couldn't start signing in. Start again.",
	statusUnconfirmed:
		"Sign-in status could not be confirmed. See whether Providers shows it signed in before trying again.",
	notConfirmed: "Sign-in has not been confirmed. See whether Providers shows it signed in before starting again.",
	checkFailed: "Could not check authorization. Retry the check when connected.",
	redirectMissing: "Paste the full redirect URL.",
	completionUnconfirmed:
		"Sign-in completion could not be confirmed. See whether Providers shows it signed in before submitting again.",
	statusCheckFailed: "Could not check credential status.",
};

/** A problem as the state carries it: its token, and its words. */
function reporting(problem: SignInProblem | null): Pick<SignInState, "problem" | "error"> {
	return { problem, error: problem === null ? null : SIGN_IN_PROBLEMS[problem] };
}

interface SignInState {
	phase: "idle" | "starting" | "device" | "browser" | "authorized" | "expired" | "error";
	device?: AuthDeviceStartResponse;
	browser?: AuthLoginStartResponse;
	busy: boolean;
	problem: SignInProblem | null;
	/** The problem in the person's terms. */
	error: string | null;
	credentialState: "configured" | "unconfigured" | "unknown";
}

// The hub supplies the device-flow poll interval, so it is untrusted input.
// Past setTimeout's 32-bit millisecond range the delay wraps and the timer
// fires at once, turning a nonsense interval into a tight poll loop; a minute
// is longer than any interval a device flow has reason to ask for.
const MAX_POLL_SECONDS = 60;

/** One sign-in flow bound to one provider instance on one hub. Its RPCs go
 * through the credential store it is handed; `connection` is an opaque token
 * for the hub connection the screen says is usable - null while there is
 * none, and a new identity when the connection was replaced, which is what
 * tells an interrupted exchange from a same-connection transition. */
export class ProviderSignIn {
	private state: SignInState = {
		phase: "idle",
		busy: false,
		...reporting(null),
		credentialState: "unknown",
	};
	private listeners = new Set<() => void>();
	private disposed = false;
	private active = true;
	private generation = 0;
	private timer?: ReturnType<typeof setTimeout>;
	private uncertain = false;
	private pendingOperation: "devicePoll" | "completion" | "status" | null = null;
	private connection: object | null = null;
	constructor(
		private readonly store: CredentialInstancesStore,
		private provider: string,
	) {}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(change: Partial<SignInState>) {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}
	private current(generation: number) {
		return !this.disposed && generation === this.generation;
	}
	private clearTimer() {
		clearTimeout(this.timer);
		this.timer = undefined;
	}
	private schedule() {
		this.clearTimer();
		if (
			this.disposed ||
			!this.connection ||
			!this.active ||
			this.state.phase !== "device" ||
			this.state.busy ||
			this.state.error ||
			this.uncertain
		)
			return;
		const interval = this.state.device?.intervalSeconds ?? 5;
		const delay = Number.isFinite(interval) ? Math.min(MAX_POLL_SECONDS, Math.max(1, interval || 5)) : 5;
		this.timer = setTimeout(() => {
			void this.poll();
		}, delay * 1000);
	}
	/** The screen's word on the hub connection: null while none is usable, a
	 * fresh token when it was replaced, the same token while it merely
	 * transitions. A change retires whatever exchange was in flight and marks
	 * it uncertain where a write may have landed. Dispose when changing hubs. */
	setConnection(connection: object | null) {
		if (this.disposed || this.connection === connection) return;
		this.clearTimer();
		this.generation += 1;
		const wasNull = this.connection === null;
		this.connection = connection;
		const interruptedWrite =
			this.state.busy &&
			(this.state.phase === "starting" || (this.state.phase === "browser" && this.pendingOperation === "completion"));
		const interruptedPoll = this.pendingOperation === "devicePoll" && this.state.phase === "device";
		this.pendingOperation = null;
		this.uncertain ||= interruptedPoll;
		// A connection arriving after a failed poll answers the question the
		// failure left open: the flow asks once on its own, rather than waiting
		// for the person to press Check again. Device flows expire in 15
		// minutes, so one automatic re-poll cannot loop.
		const failedPollWaiting =
			connection !== null && wasNull && this.state.phase === "device" && this.state.problem === "checkFailed";
		this.publish({
			busy: false,
			...(this.state.phase === "starting" ? { phase: "error" as const } : {}),
			...(interruptedWrite || interruptedPoll ? reporting("connectionChanged") : {}),
			...(failedPollWaiting ? reporting(null) : {}),
		});
		if (failedPollWaiting) {
			this.uncertain = false;
			void this.poll();
			return;
		}
		this.schedule();
	}
	setActive(active: boolean) {
		this.active = active;
		if (active) this.schedule();
		else this.clearTimer();
	}
	start = async (): Promise<void> => {
		if (this.disposed || !this.connection || this.state.busy) return;
		const generation = ++this.generation;
		this.uncertain = false;
		this.pendingOperation = null;
		this.clearTimer();
		this.publish({
			phase: "starting",
			busy: true,
			...reporting(null),
			device: undefined,
			browser: undefined,
			credentialState: "unknown",
		});
		try {
			const device = await this.store.getState().deviceStart(this.provider);
			if (!this.current(generation)) return;
			if (
				!device ||
				typeof device !== "object" ||
				(device.fallback !== undefined && typeof device.fallback !== "boolean") ||
				device.provider !== this.provider
			)
				throw new Error();
			if (device.fallback === true) {
				const browser = await this.store.getState().loginStart(this.provider);
				if (!this.current(generation)) return;
				if (
					!browser ||
					typeof browser !== "object" ||
					browser.provider !== this.provider ||
					typeof browser.flowId !== "string" ||
					!browser.flowId.trim() ||
					typeof browser.url !== "string" ||
					!this.safeURL(browser.url)
				)
					throw new Error();
				this.publish({ phase: "browser", browser, busy: false });
			} else {
				if (
					typeof device.flowId !== "string" ||
					!device.flowId.trim() ||
					typeof device.userCode !== "string" ||
					!device.userCode.trim() ||
					typeof device.verificationUrl !== "string" ||
					!this.safeURL(device.verificationUrl) ||
					typeof device.intervalSeconds !== "number" ||
					!Number.isSafeInteger(device.intervalSeconds) ||
					device.intervalSeconds < 0
				)
					throw new Error();
				this.publish({ phase: "device", device, busy: false });
				this.schedule();
			}
		} catch {
			if (this.current(generation))
				this.publish({
					phase: "error",
					busy: false,
					...reporting("startFailed"),
				});
		}
	};
	private poll = async (): Promise<void> => {
		if (
			this.disposed ||
			!this.connection ||
			!this.active ||
			this.state.busy ||
			this.state.phase !== "device" ||
			!this.state.device
		)
			return;
		this.clearTimer();
		const generation = this.generation;
		const flowId = this.state.device.flowId;
		this.pendingOperation = "devicePoll";
		this.publish({ busy: true, ...reporting(null) });
		try {
			const response = await this.store.getState().devicePoll(this.provider, flowId);
			if (!this.current(generation)) return;
			this.pendingOperation = null;
			if (response.state === "authorized") {
				if (!this.validAuthorizedStatus(response.status)) throw new Error();
				this.publish({
					phase: "authorized",
					busy: false,
					device: undefined,
					credentialState: "configured",
				});
			} else if (response.state === "expired") {
				this.publish({
					phase: "expired",
					busy: false,
					device: undefined,
					...reporting("statusUnconfirmed"),
				});
			} else if (response.state === "pending") {
				this.publish({ busy: false });
				this.schedule();
			} else if (response.state === "restart") {
				// The hub could not check the flow against its endpoint and
				// discarded it without saving anything: begin a fresh one.
				this.publish({ busy: false });
				void this.start();
			} else {
				this.uncertain = true;
				this.publish({
					busy: false,
					...reporting("notConfirmed"),
				});
			}
		} catch (cause) {
			if (this.current(generation)) {
				// A refused poll (the credential listing is still the old
				// connection's, so the store would not act on it) asks for a
				// retry, not a verdict: the device cadence asks again once the
				// listing can answer, and no failure latches.
				if (isStaleListingRefusal(cause)) {
					this.pendingOperation = null;
					this.publish({ busy: false });
					this.schedule();
					return;
				}
				this.uncertain = true;
				this.publish({
					busy: false,
					...reporting("checkFailed"),
				});
				this.pendingOperation = null;
			}
		}
	};
	retryPoll = () => {
		if (
			this.disposed ||
			!this.connection ||
			!this.active ||
			this.state.busy ||
			this.state.phase !== "device" ||
			!this.state.device
		)
			return Promise.resolve();
		this.uncertain = false;
		return this.poll();
	};
	complete = async (value: string): Promise<void> => {
		if (this.disposed || !this.connection || this.state.busy || this.state.phase !== "browser" || !this.state.browser)
			return;
		const redirectUrl = value.trim();
		if (!redirectUrl) {
			this.publish(reporting("redirectMissing"));
			return;
		}
		const generation = this.generation;
		this.pendingOperation = "completion";
		const flowId = this.state.browser.flowId;
		this.publish({ busy: true, ...reporting(null) });
		try {
			const response = await this.store.getState().loginComplete(this.provider, flowId, redirectUrl);
			if (this.current(generation)) {
				if (response.restart === true) {
					// The hub could not check the flow against its endpoint and
					// discarded it without saving anything: begin a fresh one.
					this.pendingOperation = null;
					this.publish({ busy: false });
					await this.start();
					return;
				}
				if (!this.validAuthorizedStatus(response.status)) throw new Error();
				this.pendingOperation = null;
				this.publish({
					phase: "authorized",
					busy: false,
					browser: undefined,
					credentialState: "configured",
				});
			}
		} catch {
			if (this.current(generation)) {
				// A completion that could not be confirmed may or may not have landed
				// on the hub, so the flow stays uncertain — the same conservative
				// terminal the poll catch and the unrecognized-status branch take.
				// Without it a later checkStatus reads clean and polling resumes.
				this.uncertain = true;
				this.publish({
					busy: false,
					...reporting("completionUnconfirmed"),
				});
				this.pendingOperation = null;
			}
		}
	};
	checkStatus = async (): Promise<void> => {
		if (this.disposed || !this.connection || this.state.busy) return;
		const generation = this.generation;
		this.clearTimer();
		this.pendingOperation = "status";
		this.publish({ busy: true, ...reporting(null) });
		try {
			const status = await this.store.getState().authStatus(this.provider);
			if (!this.current(generation) || !this.validStatus(status)) throw new Error();
			this.pendingOperation = null;
			this.publish({
				busy: false,
				...reporting(this.uncertain ? "statusUnconfirmed" : null),
				credentialState: status.signedIn && status.hasStoredOAuth ? "configured" : "unconfigured",
			});
			if (!this.state.error && !this.uncertain) this.schedule();
		} catch {
			if (this.current(generation)) {
				this.pendingOperation = null;
				this.publish({
					busy: false,
					...reporting("statusCheckFailed"),
				});
			}
		}
	};
	private safeURL(value: string) {
		try {
			const url = new URL(value);
			return ["https:", "http:"].includes(url.protocol) && !url.username && !url.password;
		} catch {
			return false;
		}
	}
	private validStatus(value: unknown): value is AuthStatusResponse {
		if (!value || typeof value !== "object") return false;
		const status = value as Record<string, unknown>;
		return (
			status.provider === this.provider &&
			typeof status.supported === "boolean" &&
			typeof status.signedIn === "boolean" &&
			typeof status.hasStoredOAuth === "boolean" &&
			typeof status.activeSource === "string" &&
			status.activeSource.trim().length > 0
		);
	}
	private validAuthorizedStatus(value: unknown): value is AuthStatusResponse {
		return (
			this.validStatus(value) && value.supported === true && value.signedIn === true && value.hasStoredOAuth === true
		);
	}
	dispose() {
		this.disposed = true;
		this.generation += 1;
		this.clearTimer();
		this.listeners.clear();
	}
}

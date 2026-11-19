import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, it, vi } from "vitest";
import { WireError } from "@evener/appwire-client";
import type { ModelListResponse, Thread } from "@evener/appwire-client";
import { type ConversationClientLike, createConversationService } from "./conversation/services/conversation";
import { SessionControls, sessionFacts, verifiedSetting } from "./sessionControls";

// The hub's receipt is the acknowledgement; the read that follows is
// catch-up. A read that fails must not turn an accepted action into an
// unconfirmed one, or the reader is told to retry something already done.
it.each(["rename", "compact", "setReasoningEffort"] as const)(
	"settles an acknowledged %s whose readback fails",
	async (kind) => {
		const controls = new SessionControls(
			await boundary({
				rename: async () => {},
				compact: async () => {},
				reasoning: async () => {},
			}),
			async () => {
				throw new Error("The session read failed.");
			},
			() => {},
			() => true,
			() => ({ supportsReasoning: true, reasoningEffortLevels: ["low", "high"], reasoningEffort: "low" }),
			() => true,
		);
		const settled =
			kind === "rename"
				? controls.rename("New name")
				: kind === "compact"
					? controls.compact()
					: controls.setReasoningEffort("high");
		// The hub took it, so the caller learns that and no error is left
		// claiming the action is unconfirmed.
		expect(await settled).toBe(true);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	},
);

async function boundary(actions: {
	rename?: (name: string) => Promise<void>;
	compact?: () => Promise<void>;
	shutdown?: () => Promise<void>;
	reasoning?: (effort: string) => Promise<void>;
	models?: () => Promise<ModelListResponse>;
	model?: (provider: string, model: string) => Promise<void>;
	vision?: (visionModel: string) => Promise<void>;
	forceStop?: () => Promise<void>;
	resume?: () => Promise<void>;
}) {
	const thread: Thread = {
		id: "thread",
		sessionId: "session",
		preview: "",
		ephemeral: false,
		modelProvider: "scripted",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "idle" },
		cwd: "/test",
		cliVersion: "test",
		source: "local",
		turns: [],
		evener: {
			ref: "local:test",
			instanceId: "instance",
			queue: { revision: 0 },
			capabilities: {
				send: true,
				steer: false,
				interrupt: false,
				compact: true,
				clear: false,
				forkFromTurn: false,
				shutdown: true,
				changeModel: true,
				changeVisionModel: actions.vision !== undefined,
				sharedNotes: false,
				queue: false,
				goal: false,
				rename: true,
			},
		},
	};
	const wire: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: async (method, params) => {
			if (method === "thread/read") return { thread };
			if (method === "model/list") return actions.models?.() ?? { data: [] };
			if (method === "thread/model/set") {
				const { modelProvider, model } = params as {
					modelProvider: string;
					model: string;
				};
				await actions.model?.(modelProvider, model);
				return {};
			}
			if (method === "thread/vision-model/set") {
				await actions.vision?.((params as { visionModel: string }).visionModel);
				return {};
			}
			if (method === "evener/thread/name/set") await actions.rename?.((params as { name: string }).name);
			else if (method === "thread/compact/start") await actions.compact?.();
			else if (method === "thread/shutdown") await actions.shutdown?.();
			else if (method === "evener/thread/forceStop") await actions.forceStop?.();
			else if (method === "thread/resume") await actions.resume?.();
			else if (method === "thread/reasoning-effort/set")
				await actions.reasoning?.((params as { reasoningEffort: string }).reasoningEffort);
			else throw new Error(`Unexpected method ${method}`);
			return {};
		},
		onNotification: () => () => {},
		forceStop: async () => actions.forceStop?.(),
		resumeThread: async () => {
			await actions.resume?.();
			return { thread };
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	const service = createConversationService(wire);
	await service.open("local:test");
	return service;
}

describe("conversation-owned session controls", () => {
	it("allows force stop while ordinary composer mutation is blocked", async () => {
		let stopped = 0;
		const controls = new SessionControls(
			await boundary({ forceStop: async () => {} }),
			async () => {},
			() => {
				stopped += 1;
			},
			() => true,
			() => null,
			() => false,
		);
		expect(await controls.forceStop()).toBe(true);
		expect(stopped).toBe(1);
	});

	it("shuts down without narrating it: the screen confirms (ruling 19)", async () => {
		let stopped = 0;
		const controls = new SessionControls(
			await boundary({ shutdown: async () => {} }),
			async () => {},
			() => {
				stopped += 1;
			},
			() => true,
			() => null,
			() => true,
		);
		expect(await controls.shutdown()).toBe(true);
		expect(stopped).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null, notice: null });
	});

	it("settles an acknowledged shutdown after another screen covers the conversation", async () => {
		let inFront = true;
		let resolve!: () => void;
		let stopped = 0;
		const controls = new SessionControls(
			await boundary({
				shutdown: () =>
					new Promise<void>((done) => {
						resolve = done;
					}),
			}),
			async () => {},
			() => {
				stopped += 1;
			},
			(scope) => (scope === "session" ? true : inFront),
			() => null,
			() => true,
		);
		const request = controls.shutdown();
		// An outer screen covers the conversation while the hub's reply is out.
		inFront = false;
		resolve();
		expect(await request).toBe(true);
		expect(stopped).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});

	it("does not claim force stop success when recovery fails", async () => {
		let stopped = 0;
		const controls = new SessionControls(
			await boundary({
				forceStop: async () => {
					throw new Error("offline");
				},
			}),
			async () => {},
			() => {
				stopped += 1;
			},
			() => true,
			() => null,
			() => false,
		);
		expect(await controls.forceStop()).toBe(false);
		expect(stopped).toBe(0);
		expect(controls.getSnapshot().error).not.toBeNull();
	});

	it("does not publish resume success when resume fails", async () => {
		let refreshes = 0;
		const controls = new SessionControls(
			await boundary({
				resume: async () => {
					throw new Error("offline");
				},
			}),
			async () => {
				refreshes += 1;
			},
			() => {},
			() => true,
			() => null,
			() => false,
		);
		expect(await controls.resume()).toBe(false);
		expect(refreshes).toBe(0);
		expect(controls.getSnapshot().notice).toBeNull();
	});
	// The session's ref survives a reconnect, but no thread is open on the
	// service yet. An owed resume sent into that gap reaches nobody, and the
	// restart records it as failed and stops retrying, leaving the daemon
	// stopped. It has to be refused before dispatch so the retry stays alive.
	it("refuses an owed resume before dispatch while its binding cannot send", async () => {
		let sent = 0;
		let ready = true;
		let opened = true;
		const controls = new SessionControls(
			await boundary({
				resume: async () => {
					sent += 1;
				},
			}),
			async () => {},
			() => {},
			// The binding answers: the session is this one, the transport is the
			// client, and an open thread is what decides whether a write can go
			// out. Reading needs only the first two.
			(scope) => (scope === "session" ? true : scope === "send" ? ready && opened : ready),
			() => null,
			() => true,
		);
		ready = false;
		expect(await controls.resume()).toBe(false);
		expect(sent).toBe(0);
		// Nothing was left pending, so the restart's next attempt is free to go.
		expect(controls.getSnapshot().pending).toBeNull();
		// The reconnect has restored the client and the replacement service has
		// not opened its thread yet. The resume is still owed, and sending into
		// this gap would be recorded as a failed attempt and dropped.
		ready = true;
		opened = false;
		expect(await controls.resume()).toBe(false);
		expect(sent).toBe(0);
		expect(controls.getSnapshot().pending).toBeNull();
		opened = true;
		ready = true;
		expect(await controls.resume()).toBe(true);
		expect(sent).toBe(1);
	});

	// The resume's reply is the acknowledgement, and the read after it is
	// catch-up. Another resume queued on the same connection can recycle the
	// socket while that read is out, and a rejected read then reports a resume
	// the hub already confirmed as one that could not start the session - the
	// restart records that and gives up on a daemon that is running. A read
	// that fails leaves the acknowledgement standing; the live binding's own
	// recovery brings the projection up to date.
	it("settles a resume the hub acknowledged when its catch-up read fails", async () => {
		const controls = new SessionControls(
			await boundary({ resume: async () => {} }),
			async () => {
				throw new Error("The socket recycled before the read settled.");
			},
			() => {},
			() => true,
			() => null,
			() => false,
		);
		expect(await controls.resume()).toBe(true);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});

	// A restart the reader already asked for is owed work, not a new request. A
	// root-stack push can cover a Duo conversation while the force-stop is
	// still out, and refusing the resume there leaves the daemon stopped with
	// nothing left to retry it: the restart reads the refusal and stops.
	// Pressing something new on a covered pane is still refused, because that
	// is the reader's to start and a covered pane offers nothing to press.
	it("admits an owed resume under a covering screen, and still refuses a new action there", async () => {
		let resumes = 0;
		const controls = new SessionControls(
			await boundary({
				resume: async () => {
					resumes += 1;
				},
				compact: async () => {},
			}),
			async () => {},
			() => {},
			// The pane is covered, so the reader's own fence answers no; the send
			// scope answers for the owed resume alone.
			(scope) => scope === "send" || scope === "session" || scope === "destination",
			() => null,
			() => true,
		);
		expect(await controls.compact()).toBe(false);
		expect(await controls.resume()).toBe(true);
		expect(resumes).toBe(1);
	});

	it("does not recover a stale binding", async () => {
		let attempts = 0;
		const service = await boundary({
			forceStop: async () => {
				attempts += 1;
			},
		});
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			() => false,
			() => null,
			() => false,
		);
		expect(await controls.forceStop()).toBe(false);
		expect(attempts).toBe(0);
	});

	it("does not recover after controls are disposed", async () => {
		let attempts = 0;
		const service = await boundary({
			forceStop: async () => {
				attempts += 1;
			},
		});
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			() => true,
			() => null,
			() => false,
		);
		controls.dispose();
		expect(await controls.forceStop()).toBe(false);
		expect(attempts).toBe(0);
	});
	it.each([undefined, true])("sets a catalog vision model with support metadata %j", async (supportsVision) => {
		const applied: string[] = [];
		const controls = new SessionControls(
			await boundary({
				models: async () => ({
					data: [{ provider: "one", model: "vision", supportsVision }],
				}),
				vision: async (value) => {
					applied.push(value);
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("one", "vision")).toBe(true);
		expect(applied).toEqual(["one/vision"]);
	});
	// A load the binding moved on from must not strand the controls: the next
	// load reads the catalog again, instead of returning early on a load that
	// will never publish.
	it("loads the catalog again after a load that answered once the binding had moved on", async () => {
		let current = true;
		let reads = 0;
		const answered = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					if (reads === 1) await answered.promise;
					return { data: [{ provider: "one", model: "m", displayName: "Model One" }] };
				},
			}),
			async () => {},
			() => {},
			() => current,
			() => null,
			() => true,
		);
		const first = controls.loadModels();
		current = false;
		answered.resolve();
		await first;
		current = true;
		await controls.loadModels();
		expect(reads).toBe(2);
		expect(controls.getSnapshot()).toMatchObject({
			loadingModels: false,
			catalog: { data: [{ displayName: "Model One" }] },
		});
	});
	// The model's name comes from the catalog: a reload keeps the one it has
	// until the new one lands, so the label never falls back to the raw id.
	it("keeps the last catalog while it reloads", async () => {
		let reads = 0;
		const second = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					if (reads === 2) await second.promise;
					return { data: [{ provider: "one", model: "m", displayName: `Model One ${reads}` }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		const reload = controls.loadModels();
		expect(controls.getSnapshot()).toMatchObject({
			loadingModels: true,
			catalog: { data: [{ displayName: "Model One 1" }] },
		});
		second.resolve();
		await reload;
		expect(controls.getSnapshot().catalog?.data[0]?.displayName).toBe("Model One 2");
	});

	// The hub announces a refreshed model list on evener/auth/updated (#3539):
	// a loaded catalog is read again and replaced in place, and a failed read
	// keeps it, since the list the hub last served still stands.
	it("refreshes a loaded catalog in place and keeps it when the read fails", async () => {
		let reads = 0;
		let failing = false;
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					if (failing) throw new Error("hub unavailable");
					return { data: [{ provider: "one", model: "m", displayName: `Model One ${reads}` }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		await controls.refreshModels();
		expect(controls.getSnapshot()).toMatchObject({
			loadingModels: false,
			modelError: null,
			catalog: { data: [{ displayName: "Model One 2" }] },
		});
		failing = true;
		await controls.refreshModels();
		expect(controls.getSnapshot()).toMatchObject({
			loadingModels: false,
			modelError: null,
			catalog: { data: [{ displayName: "Model One 2" }] },
		});
	});
	// Two announcements close together: the list the later read brings is the
	// one that stays, whichever read answers last.
	it("keeps the newest refresh's catalog when two overlap", async () => {
		let reads = 0;
		const first = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					const read = reads;
					if (read === 2) await first.promise;
					return { data: [{ provider: "one", model: "m", displayName: `Model One ${read}` }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		const older = controls.refreshModels();
		const newer = controls.refreshModels();
		await newer;
		first.resolve();
		await older;
		expect(reads).toBe(3);
		expect(controls.getSnapshot().catalog?.data[0]?.displayName).toBe("Model One 3");
	});
	// A load started after a refresh is the newer read: whatever it brings,
	// including a failure that clears the catalog, the older refresh landing
	// afterwards changes nothing.
	it.each([
		["fails", true],
		["succeeds", false],
	])("lets a load that %s after a refresh stand over the refresh", async (_, loadFails) => {
		let reads = 0;
		const held = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					const read = reads;
					if (read === 2) await held.promise;
					if (read === 3 && loadFails) throw new Error("hub unavailable");
					return { data: [{ provider: "one", model: "m", displayName: `Model One ${read}` }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		const refresh = controls.refreshModels();
		await controls.loadModels();
		held.resolve();
		await refresh;
		if (loadFails) expect(controls.getSnapshot()).toMatchObject({ catalog: null, modelError: "hub unavailable" });
		else expect(controls.getSnapshot().catalog?.data[0]?.displayName).toBe("Model One 3");
	});
	// An announcement that arrives while a load is out is not dropped: the load
	// may have read the list before the hub refreshed it, so the catalog is read
	// again once the load settles (Jesse, 2026-09-30).
	it("reads again after a load when an announcement arrived during it", async () => {
		let reads = 0;
		const held = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					const read = reads;
					if (read === 2) await held.promise;
					return { data: [{ provider: "one", model: "m", displayName: `Model One ${read}` }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		const load = controls.loadModels();
		await controls.refreshModels();
		expect(reads).toBe(2);
		held.resolve();
		await load;
		await vi.waitFor(() => expect(reads).toBe(3));
		await vi.waitFor(() => expect(controls.getSnapshot().catalog?.data[0]?.displayName).toBe("Model One 3"));
	});
	it("refreshes nothing before a catalog is loaded", async () => {
		let reads = 0;
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					reads++;
					return { data: [] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.refreshModels();
		expect(reads).toBe(0);
	});

	// A screen that binds a session again hands its new controls the catalog
	// it already knows, so the label doesn't wait on a read to name the model.
	it("starts from the catalog it is given", async () => {
		const known = { data: [{ provider: "one", model: "m", displayName: "Model One" }] };
		const controls = new SessionControls(
			await boundary({}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			known,
		);
		expect(controls.getSnapshot().catalog).toBe(known);
	});
	it("rejects a catalog model explicitly marked as not supporting vision", async () => {
		let attempts = 0;
		const service = await boundary({
			models: async () => ({
				data: [{ provider: "one", model: "text", supportsVision: false }],
			}),
			vision: async () => {
				attempts++;
			},
		});
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("one", "text")).toBe(false);
		expect(attempts).toBe(0);
	});
	it("refreshes once after actionUnavailable without replaying the vision mutation", async () => {
		let attempts = 0;
		const service = await boundary({
			models: async () => ({ data: [{ provider: "one", model: "vision" }] }),
			vision: async () => {
				attempts++;
				throw new WireError("Vision is unavailable", -32000, {
					evenerErrorInfo: "actionUnavailable",
				});
			},
		});
		let refreshed = 0;
		const controls = new SessionControls(
			service,
			async () => {
				refreshed++;
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("one", "vision")).toBe(false);
		expect(attempts).toBe(1);
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot().error).not.toBeNull();
	});
	// A covering route does not take the refusal away from the picker under it:
	// the detail stack keeps its local focus, so the row is still there to press
	// when the reader comes back. The binding can still read, so the read that
	// catches the picker up runs even though this pane is not the one in front.
	// The refusal itself is still what the reader is told, and nothing is sent
	// twice.
	it("reads a refused pick back when a covering route took the pane out of front", async () => {
		let inFront = true;
		let attempts = 0;
		let refreshed = 0;
		const sent = Promise.withResolvers<void>();
		const ack = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "one", model: "vision" }] }),
				vision: async () => {
					attempts++;
					sent.resolve();
					await ack.promise;
					throw new WireError("Vision is unavailable", -32000, {
						evenerErrorInfo: "actionUnavailable",
					});
				},
			}),
			async () => {
				refreshed++;
			},
			() => {},
			// The session, its transport and its binding are live, so settlement
			// and the readback both hold; the route is what the covering screen
			// takes away, and it is what the announcement reads.
			(scope) => (scope === undefined || scope === "route" ? inFront : true),
			() => null,
			() => true,
		);
		await controls.loadModels();
		const pick = controls.changeVisionModel("one", "vision");
		await sent.promise;
		inFront = false;
		ack.resolve();
		expect(await pick).toBe(false);
		expect(attempts).toBe(1);
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot().error).not.toBeNull();
	});
	it.each(["switch", "dispose", "reject"] as const)(
		"handles %s during rejected-action refresh without replay or stale publication",
		async (outcome) => {
			let current = true;
			let attempts = 0;
			const enteredRefresh = Promise.withResolvers<void>();
			const refreshed = Promise.withResolvers<void>();
			const controls = new SessionControls(
				await boundary({
					vision: async () => {
						attempts++;
						throw new WireError("Unavailable", -32000, {
							evenerErrorInfo: "actionUnavailable",
						});
					},
				}),
				async () => {
					enteredRefresh.resolve();
					await refreshed.promise;
				},
				() => {},
				() => current,
				() => null,
				() => true,
			);
			let publications = 0;
			controls.subscribe(() => {
				publications++;
			});
			const request = controls.setVisionModel("off");
			await enteredRefresh.promise;
			expect(publications).toBe(1);
			expect(await controls.setVisionModel("")).toBe(false);
			if (outcome === "switch") current = false;
			if (outcome === "dispose") controls.dispose();
			if (outcome === "reject") refreshed.reject(new Error("Disconnected"));
			else refreshed.resolve();
			expect(await request).toBe(false);
			expect(attempts).toBe(1);
			if (outcome === "reject") {
				expect(publications).toBe(2);
				expect(controls.getSnapshot().pending).toBeNull();
				expect(controls.getSnapshot().error).not.toBeNull();
			} else if (outcome === "dispose") {
				// Nothing is told to a disposed controller, but its own record has
				// to carry the rejection: the step that asked it reads the outcome
				// from here, and an unrecorded refusal reads as an acknowledgement.
				expect(publications).toBe(1);
				expect(controls.getSnapshot().pending).toBeNull();
				expect(controls.getSnapshot().error).not.toBeNull();
			} else {
				expect(publications).toBe(1);
				expect(controls.getSnapshot().error).toBeNull();
			}
		},
	);
	it("keeps one outstanding action and refreshes after rename", async () => {
		let resolve!: () => void;
		const names: string[] = [];
		let refreshed = 0;
		const controls = new SessionControls(
			await boundary({
				rename: (name) => {
					names.push(name);
					return new Promise<void>((done) => {
						resolve = done;
					});
				},
				compact: async () => {},
				shutdown: async () => {},
			}),
			async () => {
				refreshed++;
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const first = controls.rename("  Mobile session  ");
		expect(controls.getSnapshot().pending).toBe("rename");
		await controls.rename("Duplicate");
		expect(names).toEqual(["Mobile session"]);
		resolve();
		await first;
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot().pending).toBeNull();
	});
	it("does not refresh a stopped runtime or replay a failed operation", async () => {
		let stopped = 0,
			reads = 0,
			attempts = 0;
		const controls = new SessionControls(
			await boundary({
				rename: async () => {},
				compact: async () => {
					attempts++;
					throw new Error("Socket lost");
				},
				shutdown: async () => {
					stopped++;
				},
			}),
			async () => {
				reads++;
			},
			() => {
				stopped++;
			},
			() => true,
			() => null,
			() => true,
		);
		await controls.compact();
		expect(attempts).toBe(1);
		expect(controls.getSnapshot().error).not.toBeNull();
		await controls.shutdown();
		expect(stopped).toBe(2);
		expect(reads).toBe(0);
	});
	it("invalidates old confirmation callbacks and late completions when leaving a conversation", async () => {
		let resolve!: () => void;
		let left = false;
		let callbacks = 0,
			stops = 0;
		const controls = new SessionControls(
			await boundary({
				rename: async () => {},
				compact: () =>
					new Promise<void>((done) => {
						resolve = done;
					}),
				shutdown: async () => {
					stops++;
				},
			}),
			async () => {
				callbacks++;
			},
			() => {
				callbacks++;
			},
			// The reader has left the conversation, so no scope is current any
			// more: the session binding moved away from this reader.
			() => !left,
			() => null,
			() => true,
		);
		const request = controls.compact();
		left = true;
		controls.dispose();
		resolve();
		await request;
		await controls.shutdown();
		expect(stops).toBe(0);
		expect(callbacks).toBe(0);
	});
	it("rejects an old confirmation after the same service reopens on a different binding", async () => {
		let generation = 1;
		let stops = 0;
		const service = await boundary({
			shutdown: async () => {
				stops++;
			},
		});
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			() => generation === 1,
			() => null,
			() => true,
		);
		service.close();
		generation = 2;
		await service.open("local:test");
		await controls.shutdown();
		expect(stops).toBe(0);
	});
	it("refreshes the projection after compaction can resume a cold runtime", async () => {
		let refreshed = 0;
		const controls = new SessionControls(
			await boundary({ compact: async () => {} }),
			async () => {
				refreshed++;
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.compact();
		expect(refreshed).toBe(1);
	});
	it("dispatches supported reasoning once and refreshes the authoritative value", async () => {
		const settings = {
			supportsReasoning: true,
			reasoningEffort: "low",
			reasoningEffortLevels: ["low", "high"],
		};
		const requests: string[] = [];
		let resolve!: () => void;
		const controls = new SessionControls(
			await boundary({
				reasoning: async (effort) => {
					requests.push(effort);
					await new Promise<void>((done) => {
						resolve = done;
					});
				},
			}),
			async () => {
				settings.reasoningEffort = "high";
			},
			() => {},
			() => true,
			() => settings,
			() => true,
		);
		const request = controls.setReasoningEffort("high");
		await controls.setReasoningEffort("low");
		expect(requests).toEqual(["high"]);
		expect(settings.reasoningEffort).toBe("low");
		resolve();
		await request;
		expect(settings.reasoningEffort).toBe("high");
		expect(controls.getSnapshot().pending).toBeNull();
	});
	it("rejects stale or unsupported reasoning choices before dispatch", async () => {
		const settings = {
			supportsReasoning: true,
			reasoningEffort: "low",
			reasoningEffortLevels: ["low", "high"],
		};
		const requests: string[] = [];
		const controls = new SessionControls(
			await boundary({
				reasoning: async (effort) => {
					requests.push(effort);
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => settings,
			() => true,
		);
		await controls.setReasoningEffort("low");
		await controls.setReasoningEffort("invented");
		settings.reasoningEffortLevels = ["low"];
		await controls.setReasoningEffort("high");
		settings.reasoningEffortLevels = ["low", "high"];
		settings.supportsReasoning = false;
		settings.reasoningEffortLevels = [];
		await controls.setReasoningEffort("high");
		settings.supportsReasoning = true;
		controls.dispose();
		await controls.setReasoningEffort("high");
		expect(requests).toEqual([]);
	});
	it("only applies catalog provider/model pairs and serializes model changes", async () => {
		const applied: string[][] = [];
		let resolve!: () => void;
		let reads = 0;
		const controls = new SessionControls(
			await boundary({
				models: async () => ({
					data: [
						{ provider: "one", model: "shared" },
						{ provider: "two", model: "other" },
					],
				}),
				model: async (provider, model) => {
					applied.push([provider, model]);
					await new Promise<void>((done) => {
						resolve = done;
					});
				},
			}),
			async () => {
				reads++;
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		expect(await controls.changeModel("one", "shared")).toBe(false);
		await controls.loadModels();
		expect(await controls.changeModel("two", "shared")).toBe(false);
		const request = controls.changeModel("one", "shared");
		expect(await controls.changeModel("two", "other")).toBe(false);
		expect(applied).toEqual([["one", "shared"]]);
		resolve();
		expect(await request).toBe(true);
		expect(reads).toBe(1);
	});
	// An acknowledged pick can outlive the binding that sent it: the screen's
	// memo disposes the controls that are mid-pick while the same session stays
	// in front. The mutation landed, so the pick reports a change — the sheet
	// closes and the screen confirms it — instead of reading as a pick that
	// never happened. A pick that did not apply, and one whose screen left,
	// claim nothing.
	it.each([
		{ outcome: "applied while the destination stays current", mutate: "apply", destination: true, refreshes: 1 },
		{ outcome: "applied after the destination left", mutate: "apply", destination: false, refreshes: 0 },
		{ outcome: "refused while the destination stays current", mutate: "refuse", destination: true, refreshes: 0 },
	] as const)(
		"reports a pick the recycle disposed its binding for: $outcome",
		async ({ mutate, destination, refreshes }) => {
			const applied: string[] = [];
			let inFront = true;
			let current = true;
			// The binding goes, the transport does not: this is a controller
			// disposal, not a socket recycle.
			const ready = true;
			let refreshed = 0;
			const sent = Promise.withResolvers<void>();
			const ack = Promise.withResolvers<void>();
			// The session's record outlives the binding, so the destination reads
			// what the pick established from here rather than from the instance
			// that sent it.
			const session = sessionFacts(() => false);
			const controls = new SessionControls(
				await boundary({
					models: async () => ({ data: [{ provider: "two", model: "next" }] }),
					model: async (provider, model) => {
						applied.push(`${provider}/${model}`);
						sent.resolve();
						await ack.promise;
						// A refusal names what the hub would not do, so it is not a
						// write that may have applied: nothing is recorded for it.
						if (mutate === "refuse")
							throw new WireError("The hub refused the change.", -32000, {
								evenerErrorInfo: "invalidParams",
							});
					},
				}),
				async () => {
					refreshed++;
				},
				() => {},
				// The screen's fence: `inFront` is the session being the route still
				// in front, `ready` is the transport, and `current` is this
				// instance's own binding generation.
				(scope) => (scope === "route" ? inFront : inFront && (scope === "destination" ? ready : current)),
				() => null,
				() => true,
				null,
				session,
			);
			await controls.loadModels();
			const pick = controls.changeModel("two", "next");
			await sent.promise;
			expect(controls.getSnapshot().pending).toBe("changeModel");
			// The recycle: the screen's memo disposes this binding, its generation
			// moves on, and the same session stays (or leaves) the one in front.
			controls.dispose();
			current = false;
			inFront = destination;
			ack.resolve();
			expect(await pick).toBe(mutate === "apply" && destination);
			expect(applied).toEqual(["two/next"]);
			expect(refreshed).toBe(refreshes);
			// What the session's record says, which is what any binding reads. An
			// applied pick leaves the value the hub took there whichever screen is
			// current, so a replacement drawn from the projection cannot treat the
			// stale row as already set. A refusal says nothing applied, so the
			// projection still answers for that setting.
			expect(session.unread().model).toEqual(mutate === "apply" ? { state: "acknowledged", value: "two/next" } : null);
			// The other direction: a pick that did not apply still fails, and the
			// set that dispatched it keeps the hub's refusal for the sheet's own
			// report rather than swallowing the false.
			if (mutate === "refuse") expect(controls.getSnapshot().error).not.toBeNull();
		},
	);
	// A pick the hub accepted is confirmed even when the read that follows it
	// fails on the connection the pick was sent on: the acknowledgement is what
	// applied it, and the read is catch-up. The live binding still settles its
	// pending state so nothing reads as in flight afterwards.
	it("confirms an acknowledged pick whose follow-up read fails", async () => {
		const applied: string[] = [];
		let refreshed = 0;
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async (provider, model) => {
					applied.push(`${provider}/${model}`);
				},
			}),
			async () => {
				refreshed++;
				throw new Error("The read lost its connection.");
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeModel("two", "next")).toBe(true);
		expect(applied).toEqual(["two/next"]);
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});
	// A vision pick is a pick: the same acknowledgement confirms it, and its
	// readback is catch-up. Off and Session model are the same action with a
	// different value.
	it.each(["off", "", "one/vision"])("confirms an acknowledged vision pick %j whose read fails", async (value) => {
		const applied: string[] = [];
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "one", model: "vision" }] }),
				vision: async (set) => {
					applied.push(set);
				},
			}),
			async () => {
				throw new Error("The read lost its connection.");
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		const run = value === "one/vision" ? controls.changeVisionModel("one", "vision") : controls.setVisionModel(value);
		expect(await run).toBe(true);
		expect(applied).toEqual([value]);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});
	// A set the hub took whose readback never landed leaves the session's
	// projection showing the value from before it. That projection is what a
	// picker compares a choice against, so without a record of the value the
	// hub did take, the next tap on the value it still shows reads as "already
	// set" - and the sheet closes without writing the choice the reader made.
	it("records the value an acknowledged set reached the hub with when its read did not", async () => {
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {},
			}),
			async () => {
				throw new Error("The read lost its connection.");
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeModel("two", "next")).toBe(true);
		expect(controls.getSnapshot().unread).toEqual({
			model: { state: "acknowledged", value: "two/next" },
			vision: null,
		});
	});
	// A read that landed puts the projection where the set left it, so there is
	// nothing left for a picker to be misled by.
	it("records nothing when the readback landed", async () => {
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		expect(await controls.changeModel("two", "next")).toBe(true);
		expect(controls.getSnapshot().unread).toEqual({ model: null, vision: null });
	});
	// A publication is the daemon's report, not a receipt for the pick. During a
	// running turn the hub acknowledges a pick for the next turn while the
	// projection correctly still names the model that is running, so a read that
	// published has answered the pick only when it shows the picked value.
	// Treating any publication as confirmation clears the record while the
	// projection still draws the running model as selected - and the reader who
	// taps that row to undo the queued change closes the sheet without sending.
	it("keeps a next-turn pick recorded while the projection still names the running model", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			// The read landed and reports the model that is running, which is the
			// truth: the pick applies from the next turn.
			() => "one/running",
		);
		session.record("model", { state: "acknowledged", value: "two/next" });
		// The readback after the pick starts, and reports the running model.
		reads += 1;
		session.retirePublished();
		expect(session.unread()).toEqual({ model: { state: "acknowledged", value: "two/next" }, vision: null });
		// What the reader then sees: the running model's row is a correction, not
		// an already-set value, so tapping it sends the change back.
		expect(verifiedSetting(session.unread(), "model", "one/running")).toBe(false);
		// A read that does show the pick is the readback, and it retires it.
		let later = 0;
		const shown = sessionFacts(
			() => true,
			() => later,
			() => "two/next",
		);
		shown.record("model", { state: "acknowledged", value: "two/next" });
		later += 1;
		shown.retirePublished();
		expect(shown.unread()).toEqual({ model: null, vision: null });
	});
	// A read that resolves is not a read that published. The store discards a
	// projection a newer read superseded without throwing, and the refresh
	// callback sees only a store that is open with no error - so treating a
	// resolved refresh as the readback clears the record while the projection
	// still shows the value from before the set. The value the hub took has to
	// stay recorded until a projection actually lands.
	it("keeps the value an acknowledged set reached the hub with when the read published nothing", async () => {
		let published = true;
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			// The read returned normally and the store discarded it: no error,
			// and no fresh projection either.
			sessionFacts(() => published),
		);
		await controls.loadModels();
		published = false;
		expect(await controls.changeModel("two", "next")).toBe(true);
		expect(controls.getSnapshot().unread).toEqual({
			model: { state: "acknowledged", value: "two/next" },
			vision: null,
		});
	});
	// A cold pick replaces the runtime, so the controls that sent it are gone
	// before the hub's answer lands on them. A replacement built from the
	// screen's projection alone starts believing the value the pick has just
	// invalidated - and tapping that value closes the picker without writing.
	// The record therefore belongs to the session, and the replacement starts
	// from it.
	it("hands a replacement the value the session was acknowledged for", async () => {
		const session = sessionFacts(() => true);
		session.record("vision", { state: "acknowledged", value: "lunaroute/glm-5.3-vision" });
		const replacement = new SessionControls(
			await boundary({ vision: async () => {} }),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		expect(replacement.getSnapshot().unread).toEqual({
			model: null,
			vision: { state: "acknowledged", value: "lunaroute/glm-5.3-vision" },
		});
	});
	// The other direction: the retired set's own answer still has to reach the
	// session, or the replacement's clean start loses it again.
	it("records the acknowledged value on the session even when this set was replaced", async () => {
		const session = sessionFacts(() => false);
		const sent = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await sent.loadModels();
		const pick = sent.changeModel("two", "next");
		// The cold pick replaced the runtime: these controls are retired before
		// the hub's answer lands on them.
		sent.dispose();
		await pick;
		expect(session.unread()).toEqual({ model: { state: "acknowledged", value: "two/next" }, vision: null });
	});
	// A replacement is built while the write it did not send is still out, so
	// sampling the record once at construction misses the answer when it lands
	// afterwards. The picker reads the replacement's own snapshot, so a stale
	// Off row would close it without sending the corrective write, and vision
	// would stay enabled.
	it("tells a binding that already exists about a write it did not send", async () => {
		const session = sessionFacts(() => false);
		const answer = Promise.withResolvers<void>();
		const sent = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "one", model: "vision" }] }),
				vision: async () => {
					await answer.promise;
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await sent.loadModels();
		const pick = sent.changeVisionModel("one", "vision");
		// The reader's screen rebinds while the hub has not answered: this is the
		// set the picker now renders from, and it has never heard of that write.
		const replacement = new SessionControls(
			await boundary({ vision: async () => {} }),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		expect(replacement.getSnapshot().unread).toEqual({ model: null, vision: null });
		// The answer lands on the retired set, whose readback never published.
		sent.dispose();
		answer.resolve();
		await pick;
		// The replacement now says the value it would otherwise trust is unread.
		expect(replacement.getSnapshot().unread).toEqual({
			model: null,
			vision: { state: "acknowledged", value: "one/vision" },
		});
	});
	// The screen builds these controls in useMemo, and a render React discards
	// never runs the cleanup effect that would dispose them. A session watch
	// taken in the constructor would then outlive every use of the controller
	// and keep its listener on the session until the entry was retired. It
	// follows the listeners instead: the first one attaches it, the last one
	// lets it go, and the record is read again when it attaches so a write that
	// landed in between is not lost.
	it("watches the session only while a consumer is subscribed", async () => {
		const session = sessionFacts(() => true);
		const controls = new SessionControls(
			await boundary({ vision: async () => {} }),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		// Nobody is listening, so the record moving reaches nothing.
		session.record("vision", { state: "unknown" });
		expect(controls.getSnapshot().unread.vision).toEqual({ state: "unknown" });
		let told = 0;
		const unsubscribe = controls.subscribe(() => {
			told += 1;
		});
		// The first listener reads the record as it stands rather than a copy
		// sampled before the write.
		expect(told).toBe(0);
		session.record("vision", { state: "acknowledged", value: "off" });
		expect(told).toBe(1);
		expect(controls.getSnapshot().unread.vision).toEqual({ state: "acknowledged", value: "off" });
		unsubscribe();
		// The last listener leaving lets the watch go, so a settled write no
		// longer reaches a controller nobody holds.
		session.record("vision", null);
		expect(told).toBe(1);
		expect(controls.getSnapshot().unread.vision).toBeNull();
		controls.dispose();
	});
	it("starts a listener from the record a write settled on before it subscribed", async () => {
		const session = sessionFacts(() => true);
		const controls = new SessionControls(
			await boundary({ vision: async () => {} }),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		session.record("model", { state: "acknowledged", value: "two/next" });
		const seen = vi.fn();
		const unsubscribe = controls.subscribe(seen);
		expect(seen).toHaveBeenCalledOnce();
		expect(controls.getSnapshot().unread.model).toEqual({ state: "acknowledged", value: "two/next" });
		unsubscribe();
		controls.dispose();
	});
	// A write the hub never answered is not a write it refused, and it is not a
	// value either. A request that times out while the phone is reconnecting can
	// still reach the hub, and a cold set is applied inside the hub's own resume,
	// after the client has given up - so the hub may hold the new value, the old
	// one, or nothing. Recording the value the write was for would make the next
	// tap on the row that already shows it read as "already set": the reader who
	// enabled vision behind a stale Off tick and taps Off to undo it closes the
	// sheet without sending anything, and vision stays on. The outcome is
	// unknown, so the projection is not trusted for that setting at all.
	it("records an unknown outcome for a vision write that was never answered", async () => {
		const session = sessionFacts(() => true);
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				vision: async () => {
					throw new Error("The connection closed before the hub answered.");
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("two", "next")).toBe(false);
		expect(session.unread()).toEqual({ model: null, vision: { state: "unknown" } });
		expect(controls.getSnapshot().unread).toEqual({ model: null, vision: { state: "unknown" } });
		// What the reader then sees: Off is still drawn as selected, and tapping
		// it has to send the correction rather than dismiss.
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(false);
	});
	// The hub's own way of saying the same thing. It answers sessionUnavailable
	// when it lost the daemon's reply to a write it had already forwarded, so
	// this is a wire rejection whose outcome is not known - unlike a refusal,
	// which names what the hub would not do.
	it("records an unknown outcome when the hub lost the daemon's reply", async () => {
		const session = sessionFacts(() => true);
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				vision: async () => {
					throw new WireError("the daemon's reply was lost", -32000, {
						evenerErrorInfo: "sessionUnavailable",
					});
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("two", "next")).toBe(false);
		expect(session.unread()).toEqual({ model: null, vision: { state: "unknown" } });
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(false);
	});
	// The other direction, and the reason this is not simply "record on every
	// failure": a refusal is the hub's answer, so the projection still shows
	// what the hub holds and a tap on it is a genuine no-op. Recording there
	// would turn every refused pick into a redundant write.
	it("records nothing when the hub refused the vision write", async () => {
		const session = sessionFacts(() => true);
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				vision: async () => {
					throw new WireError("vision model is not available", -32000, {
						evenerErrorInfo: "actionUnavailable",
					});
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("two", "next")).toBe(false);
		expect(session.unread()).toEqual({ model: null, vision: null });
		expect(controls.getSnapshot().unread).toEqual({ model: null, vision: null });
	});
	// The two settings are independent facts. A record with room for one throws
	// the other away, and the setting it discarded goes back to trusting a
	// projection its own unanswered write may have invalidated: a named vision
	// pick that timed out, then a model pick that timed out too, and the Off row
	// is selectable again while vision may still be on.
	it("keeps a vision write's record through a later model write", async () => {
		const session = sessionFacts(() => true);
		let models = 0;
		const controls = new SessionControls(
			await boundary({
				models: async () => ({
					data: [
						{ provider: "two", model: "next" },
						{ provider: "one", model: "vision" },
					],
				}),
				vision: async () => {
					throw new Error("The connection closed before the hub answered.");
				},
				model: async () => {
					models += 1;
					throw new Error("The connection closed before the hub answered.");
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await controls.loadModels();
		expect(await controls.changeVisionModel("one", "vision")).toBe(false);
		expect(await controls.changeModel("two", "next")).toBe(false);
		expect(models).toBe(1);
		// Neither reply arrived, so neither setting has an answer: the later
		// model write leaves the vision write's unknown outcome in place, and the
		// two are recorded apart rather than one overwriting the other.
		expect(session.unread()).toEqual({ model: { state: "unknown" }, vision: { state: "unknown" } });
	});
	// The unknown is a doubt, not a fact, so the read that settles the doubt has
	// to retire it. Without that, the doubt outlives every read: a vision write
	// loses its reply, a reconnect establishes that vision is still Off, and the
	// Off row the projection already draws as selected still sends a setter -
	// which on a stopped session starts the daemon to reapply what it has.
	it("retires an unknown outcome once a later read establishes the setting", async () => {
		let reads = 0;
		let published = false;
		const session = sessionFacts(
			() => published,
			() => reads,
		);
		session.record("vision", { state: "unknown" });
		// The read that establishes the setting starts after the write was
		// recorded, and its publication is what answers the doubt.
		reads += 1;
		published = true;
		session.retirePublished();
		expect(session.unread()).toEqual({ model: null, vision: null });
		// What the reader then sees: the Off row matches the projection, so a tap
		// on it is a verified no-op and the picker closes without writing.
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(true);
	});
	// A model pick sent while a turn runs is queued for the next one, and the
	// daemon's projection then names the running model whether it took the pick
	// or never saw it. A read taken during that turn therefore cannot say the
	// lost write did not apply: retiring the doubt on it lets the reader tap the
	// running model's row and close the picker while the queued switch is still
	// waiting for the next turn. Only a read that found the session between
	// turns can answer it.
	it("keeps an unanswered model write that a read during a turn cannot answer", async () => {
		let reads = 0;
		let running = true;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "one/running",
			() => running,
		);
		session.record("model", { state: "unknown" });
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toEqual({ state: "unknown" });
		expect(verifiedSetting(session.unread(), "model", "one/running")).toBe(false);
		// The turn ends, and the read begun after it reports the session at rest:
		// the queued switch has been applied by then or never reached the hub, so
		// the projection is the answer again.
		running = false;
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toBeNull();
	});
	// A vision write is never queued for a turn, so a turn says nothing about
	// it and a newer read answers it either way.
	it("retires an unanswered vision write during a turn", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "off",
			() => true,
		);
		session.record("vision", { state: "unknown" });
		reads += 1;
		session.retirePublished();
		expect(session.unread().vision).toBeNull();
	});
	// The daemon acknowledges a pick it queues for the turn boundary, and the
	// boundary can refuse it: it re-checks the settled history against the
	// picked model's protocol, and a pick whose content the new protocol cannot
	// represent is dropped with a transcript warning instead of applied. Every
	// later read still names the model that ran, so an equality-only answer
	// would keep the refused pick outstanding for good - and a tap on the row
	// showing that running model, which the reader means as a no-op, sends
	// another switch and can resume a session the reader has since stopped.
	// Once no turn is in flight the queued switch has had its boundary, so a
	// newer read naming another model has answered the record.
	it("retires an acknowledged model pick the settled read contradicts", async () => {
		let reads = 0;
		let running = true;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "one/running",
			() => running,
		);
		session.record("model", { state: "acknowledged", value: "two/next" });
		// A read during the turn names the running model, which is the truth
		// either way: the queued switch can still apply at the boundary.
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toEqual({ state: "acknowledged", value: "two/next" });
		// The turn ends and the boundary drops the pick; a read begun after it
		// names the model that ran, so the record is answered.
		running = false;
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toBeNull();
		// What the reader then sees: the running model's row is already set, so
		// a tap on it is a no-op rather than a redundant switch.
		expect(verifiedSetting(session.unread(), "model", "one/running")).toBe(true);
	});
	// The picked model does apply at the boundary, and the same read retires
	// the record for the same reason: an acknowledged pick is answered by a
	// publication that has had its boundary.
	it("retires an acknowledged model pick the settled read confirms", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "two/next",
			() => false,
		);
		session.record("model", { state: "acknowledged", value: "two/next" });
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toBeNull();
	});
	// A record built with no answer to give cannot tell a queued switch from a
	// refused one, so it keeps the equality answer a caller with no store has
	// always had: only a projection naming the pick establishes it.
	it("keeps a mismatched acknowledged model pick when the record cannot know of a turn", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "one/running",
		);
		session.record("model", { state: "acknowledged", value: "two/next" });
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toEqual({ state: "acknowledged", value: "two/next" });
	});
	// A record built with no answer to give cannot tell a queued switch from a
	// write that never applied, and keeps the answer a caller with no store has
	// always had: the next publication.
	it("retires an unanswered model write on the next read when it cannot know of a turn", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "one/running",
		);
		session.record("model", { state: "unknown" });
		reads += 1;
		session.retirePublished();
		expect(session.unread().model).toBeNull();
	});
	// The other direction, and the reason the record counts reads rather than
	// trusting the next publication: a read already out when the write was
	// recorded may have sampled the session before that write, so its answer
	// cannot clear the doubt. Retiring on any publication would put the stale
	// Off row back and reproduce the silent dismissal the record exists to stop.
	it("keeps an unknown outcome that a read already out cannot answer", async () => {
		let reads = 1;
		const session = sessionFacts(
			() => true,
			() => reads,
			// An unknown is answered by any newer publication, so its fence is
			// about the read count alone.
			() => "off",
		);
		// The write is recorded while that read is in flight.
		session.record("vision", { state: "unknown" });
		// Its publication lands without a newer read having started.
		session.retirePublished();
		expect(session.unread()).toEqual({ model: null, vision: { state: "unknown" } });
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(false);
	});
	// The fence belongs to the write, not to the setting. Two vision writes can
	// both lose their replies, with a read starting between them; the second
	// record matching the first is not evidence they are one write. If it kept
	// the first one's fence, that intervening read - which may have sampled the
	// session before the second write applied - could publish and retire both
	// doubts, putting the stale Off row back within one round trip of the second
	// lost reply.
	it("keeps the fence of a second unanswered write with a read between them", async () => {
		let reads = 0;
		const session = sessionFacts(
			() => true,
			() => reads,
			() => "off",
		);
		session.record("vision", { state: "unknown" });
		// A read starts, and a second vision write loses its reply behind it.
		reads += 1;
		session.record("vision", { state: "unknown" });
		// That read publishes: it may have sampled the session before the second
		// write, so it answers the first doubt and not the second.
		session.retirePublished();
		expect(session.unread()).toEqual({ model: null, vision: { state: "unknown" } });
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(false);
		// The read the second write deserves is the one begun after it.
		reads += 1;
		session.retirePublished();
		expect(session.unread()).toEqual({ model: null, vision: null });
	});
	// The hub's acknowledgement, not the read that follows it, is the record
	// that the pick applied. A phone that reconnects while that read is still
	// out has not left the session it is looking at, so the pick still reports
	// the change; the reconnect read is what brings it on screen.
	it("confirms an acknowledged pick when the transport reconnects before its read settles", async () => {
		let ready = true;
		let refreshed = 0;
		const started = Promise.withResolvers<void>();
		const reading = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {},
			}),
			async () => {
				refreshed++;
				started.resolve();
				await reading.promise;
			},
			() => {},
			// The screen's fence: presence is route and identity alone, and
			// `ready` is the transport.
			(scope) => (scope === "route" ? true : ready),
			() => null,
			() => true,
		);
		await controls.loadModels();
		const pick = controls.changeModel("two", "next");
		await started.promise;
		// The socket goes away while the confirmation read is still out.
		ready = false;
		reading.resolve();
		expect(await pick).toBe(true);
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, error: null });
	});
	// Settlement is not visibility: an outer screen can cover this conversation
	// without disposing its controls, and whatever was in flight still has to
	// end, or the picker under that screen stays disabled for good.
	// An acknowledgement is not a read. The hub took the value, but the other
	// mounted view of this session still draws the projection's Off row as
	// selected, and only the shared record tells it that row is no longer the
	// truth. Waiting for the readback to write that down leaves the second view
	// dismissing the picker on a row the hub no longer holds, and vision stays
	// on with nothing sent to undo it.
	it("records an acknowledged vision change before its readback lands", async () => {
		const readback = Promise.withResolvers<void>();
		const entered = Promise.withResolvers<void>();
		const session = sessionFacts(
			() => false,
			() => 0,
			() => "off",
		);
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "one", model: "vision" }] }),
				vision: async () => {},
			}),
			async () => {
				entered.resolve();
				await readback.promise;
			},
			() => {},
			() => true,
			() => null,
			() => true,
			null,
			session,
		);
		await controls.loadModels();
		const pick = controls.changeVisionModel("one", "vision");
		await entered.promise;
		// The readback is still out and the projection still says Off. The other
		// view is deciding whether its Off row may dismiss on that match.
		expect(verifiedSetting(session.unread(), "vision", "off")).toBe(false);
		expect(verifiedSetting(session.unread(), "vision", "one/vision")).toBe(true);
		readback.resolve();
		await pick;
	});
	it.each([
		{ name: "a successful rename", settle: "applies" },
		{ name: "a rejected rename", settle: "refuses" },
	] as const)("settles the in-flight state of a covered controller after $name", async ({ settle }) => {
		let covered = false;
		let refreshed = 0;
		const controls = new SessionControls(
			await boundary({
				rename: async () => {
					if (settle === "refuses") throw new Error("The hub refused the name.");
				},
			}),
			async () => {
				refreshed++;
			},
			() => {},
			// The screen's own fence: the session and its transport are live and
			// can read, and an outer screen covering the pane is what only the
			// route answer loses.
			(scope) => (scope === undefined || scope === "route" ? !covered : true),
			() => null,
			() => true,
		);
		const rename = controls.rename("New name");
		covered = true;
		// The hub's word is what settles the rename, so a covered editor does not
		// stay dirty over a name the hub took, and its readback still runs. The
		// notice is the reader's: nobody in front is owed one.
		expect(await rename).toBe(settle === "applies");
		expect(refreshed).toBe(settle === "applies" ? 1 : 0);
		expect(controls.getSnapshot().pending).toBeNull();
		expect(controls.getSnapshot().notice).toBeNull();
	});
	// The reply can arrive before the push that replaces these controls, and the
	// readback outlives both. The hub took the name, so the sheet that asked has
	// to learn that: reporting a refusal here would leave its editor open over a
	// name the hub already has, and a cold session showing its old name.
	it("settles an acknowledged rename whose readback outlives the controls", async () => {
		const started = Promise.withResolvers<void>();
		const reading = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({ rename: async () => {} }),
			async () => {
				started.resolve();
				await reading.promise;
			},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const rename = controls.rename("New name");
		await started.promise;
		// The push lands while the readback is still out.
		controls.dispose();
		reading.resolve();
		expect(await rename).toBe(true);
	});
	// A rename acknowledged after a push replaced the controls still has to
	// read the session back: a cold rename lands in the hub's past index, so a
	// session whose read is still considered current never fetches the new
	// title, and the sheet shows the old name on return.
	it("reads an acknowledged rename back after its controls were replaced", async () => {
		let refreshed = 0;
		const controls = new SessionControls(
			await boundary({ rename: async () => {} }),
			async () => {
				refreshed++;
			},
			() => {},
			// The session and its transport outlive the push; the controls do not.
			() => true,
			() => null,
			() => true,
		);
		const rename = controls.rename("New name");
		controls.dispose();
		expect(await rename).toBe(true);
		expect(refreshed).toBe(1);
	});

	// The reader can leave while the pick's refresh is still out. Whoever left
	// is owed no confirmation on the screen they left, so the pick reports no
	// change there — including when that refresh itself fails.
	it.each([{ settle: "resolves" }, { settle: "rejects" }] as const)(
		"claims no pick for a destination that left while its refresh %s",
		async ({ settle }) => {
			let inFront = true;
			let current = true;
			const ready = true;
			const sent = Promise.withResolvers<void>();
			const ack = Promise.withResolvers<void>();
			const started = Promise.withResolvers<void>();
			const reading = Promise.withResolvers<void>();
			const controls = new SessionControls(
				await boundary({
					models: async () => ({ data: [{ provider: "two", model: "next" }] }),
					model: async () => {
						sent.resolve();
						await ack.promise;
					},
				}),
				async () => {
					started.resolve();
					await reading.promise;
				},
				() => {},
				(scope) => (scope === "route" ? inFront : inFront && (scope === "destination" ? ready : current)),
				() => null,
				() => true,
			);
			await controls.loadModels();
			const pick = controls.changeModel("two", "next");
			await sent.promise;
			// The recycle disposes this binding; the session is still in front when
			// the confirmation read starts.
			controls.dispose();
			current = false;
			ack.resolve();
			await started.promise;
			// The reader leaves while that read is out.
			inFront = false;
			if (settle === "rejects") reading.reject(new Error("The read lost its connection."));
			else reading.resolve();
			expect(await pick).toBe(false);
		},
	);
	// The readback belongs to the live session and its transport, not to the
	// reader's presence: a pane another screen covers still reads the new choice
	// back, so the picker shows it on return. The announcement stays off, since
	// nobody is looking at it.
	it("reads a covered pick back without announcing it", async () => {
		let inFront = true;
		let refreshed = 0;
		const sent = Promise.withResolvers<void>();
		const ack = Promise.withResolvers<void>();
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "two", model: "next" }] }),
				model: async () => {
					sent.resolve();
					await ack.promise;
				},
			}),
			async () => {
				refreshed++;
			},
			() => {},
			// A covered pane: another screen is in front, while the session and its
			// transport are live and still able to read it.
			(scope) => (scope === "route" ? inFront : true),
			() => null,
			() => true,
		);
		await controls.loadModels();
		const pick = controls.changeModel("two", "next");
		await sent.promise;
		inFront = false;
		ack.resolve();
		// The pick reads back so the picker shows the new choice on return, and
		// says nothing to a reader who is not looking at it.
		expect(await pick).toBe(false);
		expect(refreshed).toBe(1);
		expect(controls.getSnapshot()).toMatchObject({ pending: null, notice: null });
	});
	it("keeps a covered picker's catalog so its choices are there on return", async () => {
		let inFront = true;
		const started = Promise.withResolvers<void>();
		const reply = Promise.withResolvers<ModelListResponse>();
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					started.resolve();
					return await reply.promise;
				},
			}),
			async () => {},
			() => {},
			// The picker's first catalog is being read when another screen opens
			// over it. The session and its transport are still live, so the reply
			// is this pane's to keep. Both the route question and the plain
			// "starting an action" fence follow visibility here, the way the
			// screen answers them.
			(scope) => (scope === "session" || scope === "destination" ? true : inFront),
			() => null,
			() => true,
		);
		const loading = controls.loadModels();
		await started.promise;
		inFront = false;
		reply.resolve({ data: [{ provider: "two", model: "next" }] });
		await loading;
		// Reading choices back is not an announcement: the reader finds them, and
		// nothing was said to a reader who is not looking at the sheet.
		expect(controls.getSnapshot().catalog).toMatchObject({ data: [{ provider: "two", model: "next" }] });
		expect(controls.getSnapshot().loadingModels).toBe(false);
	});
	it("ignores a catalog that resolves after the controller is disposed", async () => {
		let resolve!: (value: ModelListResponse) => void;
		const controls = new SessionControls(
			await boundary({
				models: () =>
					new Promise((done) => {
						resolve = done;
					}),
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		const request = controls.loadModels();
		controls.dispose();
		resolve({ data: [{ provider: "old", model: "model" }] });
		await request;
		expect(controls.getSnapshot().catalog).toBeNull();
	});
	it("clears old choices on catalog failure and permits an explicit retry", async () => {
		let unavailable = false;
		let attempts = 0;
		const controls = new SessionControls(
			await boundary({
				models: async () => {
					attempts++;
					if (unavailable) throw new Error("Offline");
					return { data: [{ provider: "one", model: "model" }] };
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => true,
		);
		await controls.loadModels();
		unavailable = true;
		await controls.loadModels();
		expect(controls.getSnapshot().catalog).toBeNull();
		expect(controls.getSnapshot().modelError).not.toBeNull();
		expect(attempts).toBe(2);
		unavailable = false;
		await controls.loadModels();
		expect(controls.getSnapshot().catalog?.data).toHaveLength(1);
		expect(controls.getSnapshot().modelError).toBeNull();
	});
	it("does not mutate model settings while a composer submission owns the session", async () => {
		let composing = true;
		const applied: string[] = [];
		const controls = new SessionControls(
			await boundary({
				models: async () => ({ data: [{ provider: "one", model: "model" }] }),
				model: async (_, model) => {
					applied.push(model);
				},
			}),
			async () => {},
			() => {},
			() => true,
			() => null,
			() => !composing,
		);
		await controls.loadModels();
		expect(await controls.changeModel("one", "model")).toBe(false);
		expect(applied).toEqual([]);
		composing = false;
		expect(await controls.changeModel("one", "model")).toBe(true);
		expect(applied).toEqual(["model"]);
	});
});

it("restores session-default reasoning and uses the current web fallback ladder", async () => {
	const requests: string[] = [];
	const settings = {
		supportsReasoning: true,
		reasoningEffort: "high",
		reasoningEffortLevels: [] as string[],
	};
	const controls = new SessionControls(
		await boundary({
			reasoning: async (effort) => {
				requests.push(effort);
			},
		}),
		async () => {},
		() => {},
		() => true,
		() => settings,
		() => true,
	);
	await controls.setReasoningEffort("");
	await controls.setReasoningEffort("medium");
	await controls.setReasoningEffort("invented");
	expect(requests).toEqual(["", "medium"]);
});

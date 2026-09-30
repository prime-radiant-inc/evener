import { createStore } from "zustand/vanilla";
import {
	buildComposerInput,
	MAX_ATTACHMENTS,
	markerText,
	mutationErrorData,
	pluginSelectionFromOverrides,
	pluginSelectionIssues,
	resolveScalars,
	stripMarker,
	WireError,
} from "@evener/appwire-client";
import type { LaunchConfigLayer, ModelDescriptor, Thread } from "@evener/appwire-client";
import { LOCAL_HOST } from "../../cmd/evener-hub/frontend/src/stores/hostRouting";
import type { NewSessionService } from "../../mobile/src/services/newSession";
import { type CreationDraft, type CreationDraftRepository, creationDraftMetadata } from "./creationDraftRepository";
import { type DraftImageData, imageInput } from "./draftImages";
import {
	type LaunchSetup,
	modelFromId,
	moveToHost,
	type SessionSeed,
	setupOf,
	withOwnedOverrides,
} from "./newSession/launchSetup";

/** Why Start holds a draft whose start may exist. Every message that leaves
 * such a draft ends with it, so the form shows one line that says what to do. */
const MAY_HAVE_STARTED = "It may have started: check the Board before starting this draft again, or change the draft.";
/** A new connection took over mid-start, or a draft reopened with such a start. */
const START_UNCONFIRMED = `Creation could not be confirmed. ${MAY_HAVE_STARTED}`;
const EARLIER_START_UNCONFIRMED = `An earlier creation could not be confirmed. ${MAY_HAVE_STARTED}`;
/** A start landed but its draft couldn't be cleared: the draft stays, held,
 * and this says why for as long as it does. */
const CREATED_NOT_CLEARED =
	"The session was created, but this draft couldn't be cleared from the device. Check the Board before starting it again, or change the draft.";

/** Where a hub's store keeps its draft: the device's draft storage in the app,
 * a double in tests. */
export type DraftStorage = () => Pick<CreationDraftRepository, "read" | "write" | "clear">;

type Outcome = { status: "created"; hubId: string; thread: Thread } | { status: "blocked" | "failed" | "obsolete" };
interface Form {
	storageLoaded: boolean;
	storageError: string | null;
	unconfirmedCreation: boolean;
	retryStorage(): void;
	/** The host the session starts on: "local" is the hub's own machine. */
	source: string;
	/** What moved when the host did (ruling 17), for the Host row's footer. */
	hostNote: string | null;
	/** A host change is still asking the new host whether it has the project,
	 * so the project may be the old host's: Start waits (Review Focus 1). */
	movingHost: boolean;
	cwd: string;
	prompt: string;
	images: DraftImageData[];
	addImage(image: DraftImageData): void;
	removeImage(id: string): void;
	model: ModelDescriptor | null;
	reasoning: string;
	launchOverrides: LaunchConfigLayer;
	setLaunchOverrides(value: LaunchConfigLayer): void;
	projects: string[];
	models: ModelDescriptor[];
	/** The host's recently used models, from model/list's `recent`. */
	recentModels: ModelDescriptor[];
	loadingModels: boolean;
	submitting: boolean;
	error: string | null;
	metadataError: string | null;
	modelError: string | null;
	bind(service: NewSessionService | null): void;
	setCwd(value: string, refresh?: boolean): Promise<void>;
	setPrompt(value: string): void;
	selectModel(value: ModelDescriptor | null): void;
	setReasoning(value: string): void;
	loadMetadata(): Promise<void>;
	loadModels(refresh?: boolean): Promise<void>;
	/** Reads the form's loaded model list again after the hub announced a
	 * refreshed one (evener/auth/updated), in place: the list stays on screen
	 * until the new one lands, and a failed read keeps it. */
	refreshModels(): Promise<void>;
	submit(): Promise<Outcome>;
	changeHost(host: string, hostLabel: string): Promise<void>;
	applySetup(setup: LaunchSetup): void;
	applySeed(seed: SessionSeed): void;
	/** Cancel's "Delete draft": the saved draft goes and the form empties. */
	discard(): void;
	/** A start of this very draft may already exist (it couldn't be
	 * confirmed), so starting it again could make a second session. Changing
	 * the draft makes it a new start. */
	startMayRepeat(): boolean;
	/** The hub was removed: nothing of this store's lands or starts again. */
	retired: boolean;
	retire(): void;
}
/** The place a model list answers for. */
function modelContext(source: string, cwd: string): string {
	return JSON.stringify([source, cwd.trim()]);
}

export function creationModel(
	models: ModelDescriptor[],
	selected: ModelDescriptor | null,
	overrides: LaunchConfigLayer,
): ModelDescriptor | null {
	const id = overrides.model?.trim();
	if (!id) return selected;
	return modelFromId(id, models);
}

/** The setup a start runs with, for this phone to remember: a per-launch
 * model or effort wins over the form's, as it does on the wire
 * (resolveScalars), so the next New session opens on what actually started. */
export function startedSetup(form: {
	source: string;
	cwd: string;
	models: ModelDescriptor[];
	model: ModelDescriptor | null;
	reasoning: string;
	launchOverrides: LaunchConfigLayer;
}): LaunchSetup {
	const model = creationModel(form.models, form.model, form.launchOverrides);
	const override = form.launchOverrides.reasoningEffort?.trim();
	const reasoning = override || (model?.reasoningEffortLevels?.includes(form.reasoning) ? form.reasoning : "");
	return setupOf({ ...form, model, reasoning });
}

/** The per-launch overrides without a model or effort of their own: the
 * form's model and effort are the choice, so a stale override can never
 * quietly outrank one the person just made or applied. */
function withoutModelChoice(layer: LaunchConfigLayer): LaunchConfigLayer {
	const next = { ...layer };
	delete next.model;
	delete next.reasoningEffort;
	return next;
}

export function createNewSessionStore(hubId: string, storage?: DraftStorage) {
	let service: NewSessionService | null = null;
	let connection = 0;
	let catalog = 0;
	let refreshingModels = false;
	let loadedContext: string | null = null;
	let creationRequested = false;
	let saving = false;
	let lastSaved = "";
	// Bumped whenever the form moves (movePlacement), so a host change whose
	// answers arrive after the form moved again drops them (Review Focus 2).
	let placement = 0;
	// Bumped by every host change and every move, so only the newest host
	// change says when it has finished placing the project.
	let hostMoves = 0;
	// A session's model named the way it reports it, waiting for the host's
	// model list to find it (applySeed). A move drops it, so it never lands in
	// another project's or host's list.
	let pendingModelId: string | null = null;
	// The draft's content when its start became unconfirmed (isUnconfirmedDraft),
	// moved along by the host's own fill-ins, never by the person's edits.
	let unconfirmedContent: string | null = null;
	/** Runs `fn` with the draft's autosave off, for changes the store saves
	 * itself or must not save at all. */
	function withoutSaving<T>(fn: () => T): T {
		saving = true;
		try {
			return fn();
		} finally {
			saving = false;
		}
	}
	/** The form moves: to another host or project, the latest start, a seed, or an
	 * empty form after a start. Answers for the old place are dropped, a
	 * session's model waiting for them goes, and a host change still answering
	 * stops placing the project. */
	function movePlacement(): number {
		hostMoves++;
		pendingModelId = null;
		store.setState({ movingHost: false });
		return ++placement;
	}
	/** Puts a model list read for the form's host and project on the form:
	 * the chosen model stays when the list still has it (or a seeded one takes
	 * its place), and the effort stays when that model still offers it. */
	function listModels(
		result: { data: ModelDescriptor[]; recent?: ModelDescriptor[] },
		selection: ModelDescriptor | null,
		reasoning: string,
	) {
		const seeded = pendingModelId === null ? null : modelFromId(pendingModelId, result.data);
		pendingModelId = null;
		const model =
			seeded ??
			result.data.find((item) => item.provider === selection?.provider && item.model === selection.model) ??
			null;
		const settingsModel = creationModel(result.data, model, store.getState().launchOverrides);
		// The host's list filling in or clearing the model and effort isn't
		// the person editing the draft: a draft whose start may exist stays
		// that draft (#3104).
		const listed = {
			models: result.data,
			recentModels: result.recent ?? [],
			model,
			reasoning: settingsModel?.reasoningEffortLevels?.includes(reasoning) ? reasoning : "",
			modelError: null,
		};
		if (isUnconfirmedDraft()) {
			withoutSaving(() => store.setState(listed));
			unconfirmedContent = draftContent();
			saveDraft();
		} else store.setState(listed);
	}
	const store = createStore<Form>((set, get) => ({
		storageLoaded: !storage,
		storageError: null,
		unconfirmedCreation: false,
		retryStorage() {
			if (get().storageLoaded) saveDraft();
			else {
				restoreDraft();
				if (get().storageLoaded) void get().loadModels(true);
			}
		},
		source: LOCAL_HOST,
		hostNote: null,
		movingHost: false,
		cwd: "",
		prompt: "",
		images: [],
		addImage(image) {
			const state = get();
			if (state.submitting) return;
			if (
				state.images.length >= MAX_ATTACHMENTS ||
				state.images.some((item) => item.id === image.id || item.marker === image.marker)
			)
				throw new Error("This image cannot be added to the draft.");
			set({
				images: [...state.images, { ...image }],
				prompt: state.prompt + markerText(image.marker),
			});
		},
		removeImage(id) {
			const state = get();
			if (state.submitting) return;
			const image = state.images.find((item) => item.id === id);
			if (!image) return;
			set({
				images: state.images.filter((item) => item.id !== id),
				prompt: stripMarker(state.prompt, undefined, image.marker).value,
			});
		},
		model: null,
		reasoning: "",
		launchOverrides: {},
		setLaunchOverrides(value) {
			if (!get().submitting) set({ launchOverrides: JSON.parse(JSON.stringify(value)) });
		},
		projects: [],
		models: [],
		recentModels: [],
		loadingModels: false,
		submitting: false,
		error: null,
		metadataError: null,
		modelError: null,
		bind(next) {
			if (service === next) return;
			const uncertainCreation = get().submitting && creationRequested;
			creationRequested = false;
			service = next;
			connection++;
			refreshingModels = false;
			loadedContext = null;
			catalog++;
			hostMoves++;
			set({
				movingHost: false,
				projects: [],
				models: [],
				recentModels: [],
				loadingModels: false,
				submitting: false,
				...(uncertainCreation
					? {
							error: START_UNCONFIRMED,
						}
					: {}),
			});
		},
		async setCwd(cwd, refresh = true) {
			if (get().submitting) return;
			// A project chosen here answers the host-change line (ruling 17).
			if (cwd.trim() === get().cwd.trim()) {
				set({ cwd, hostNote: null });
				if (refresh) await get().loadModels();
				return;
			}
			movePlacement();
			loadedContext = null;
			catalog++;
			refreshingModels = false;
			set({
				cwd,
				hostNote: null,
				models: [],
				recentModels: [],
				model: null,
				reasoning: "",
				loadingModels: false,
			});
			if (refresh) await get().loadModels();
		},
		setPrompt(prompt) {
			if (get().submitting) return;
			set({ prompt });
		},
		selectModel(value) {
			if (get().submitting) return;
			const model = get().models.find((m) => m.provider === value?.provider && m.model === value.model) ?? null;
			const reasoning = get().launchOverrides.reasoningEffort || get().reasoning;
			const launchOverrides = withoutModelChoice(get().launchOverrides);
			set({
				model,
				launchOverrides,
				reasoning: model?.reasoningEffortLevels?.includes(reasoning) ? reasoning : "",
			});
		},
		setReasoning(value) {
			if (get().submitting) return;
			const state = get();
			const model = creationModel(state.models, state.model, state.launchOverrides);
			const launchOverrides = { ...state.launchOverrides };
			delete launchOverrides.reasoningEffort;
			set({
				launchOverrides,
				reasoning: model?.reasoningEffortLevels?.includes(value) ? value : "",
			});
		},
		async loadMetadata() {
			const current = service;
			const generation = connection;
			const host = get().source;
			if (!current) return;
			try {
				const projects = await current.recentProjects(host);
				// Another host's projects never land in this one's form.
				if (generation === connection && get().source === host) set({ projects, metadataError: null });
			} catch {
				if (generation === connection && get().source === host)
					set({
						metadataError: "Couldn't load this host's recent projects.",
					});
			}
		},
		async loadModels(refresh = false) {
			const current = service;
			const { cwd, source } = get();
			const context = modelContext(source, cwd);
			if (current && loadedContext === context && !refresh) return;
			const selection = get().model;
			const reasoning = get().reasoning;
			loadedContext = null;
			const generation = ++catalog;
			refreshingModels = !!current && refresh;
			set({
				models: [],
				recentModels: [],
				loadingModels: !!current,
			});
			if (!current) return;
			try {
				const result = await current.models(cwd.trim() ? { cwd: cwd.trim() } : {}, source);
				if (generation === catalog) {
					loadedContext = context;
					listModels(result, selection, reasoning);
				}
			} catch {
				if (generation === catalog)
					set({
						modelError: "Couldn't load this host's models. The hub's default model still works.",
					});
			} finally {
				if (generation === catalog) {
					refreshingModels = false;
					set({ loadingModels: false });
				}
			}
		},
		async refreshModels() {
			const current = service;
			const { cwd, source } = get();
			// Only a list on screen for the form's host and project can be kept
			// up; a load in flight is already reading, and a start takes the form
			// as it was sent, the way every other list change waits for it.
			if (!current || loadedContext !== modelContext(source, cwd) || get().loadingModels || get().submitting)
				return;
			const generation = ++catalog;
			try {
				const result = await current.models(cwd.trim() ? { cwd: cwd.trim() } : {}, source);
				if (generation === catalog && !get().submitting) listModels(result, get().model, get().reasoning);
			} catch {
				// A failed refresh keeps the list the hub last served.
			}
		},
		async submit() {
			const current = service;
			const generation = connection;
			const { source, cwd, prompt, model, reasoning, submitting, launchOverrides } = get();
			if (
				!current ||
				get().retired ||
				submitting ||
				// A start of this very draft may exist: starting it again could
				// make a second session (the form's Start only says so).
				get().startMayRepeat() ||
				get().movingHost ||
				refreshingModels ||
				!cwd.trim() ||
				!get().storageLoaded ||
				(model !== null && loadedContext !== modelContext(source, cwd))
			)
				return { status: "blocked" };
			const settingsModel = creationModel(get().models, model, launchOverrides);
			const input = buildComposerInput(
				prompt,
				get().images.map((image) => imageInput(image, image.data)),
			);
			const scalars = resolveScalars(
				{
					model: model?.model,
					modelProvider: model?.provider,
					reasoningEffort: settingsModel?.reasoningEffortLevels?.includes(reasoning) ? reasoning : undefined,
				},
				launchOverrides,
			);
			set({ submitting: true, error: null });
			let startDispatched = false;
			creationRequested = false;
			try {
				const pluginSelection = pluginSelectionFromOverrides(launchOverrides);
				if (pluginSelection.mode === "explicit") {
					const preview = await current.previewPlugins(
						{
							cwd: cwd.trim(),
							launchOverrides,
						},
						source,
					);
					if (generation !== connection) return { status: "obsolete" };
					const issues = pluginSelectionIssues(pluginSelection, preview);
					if (issues.length) {
						set({
							error: issues.map((issue) => `${issue.name}: ${issue.reason}`).join("\n"),
						});
						return { status: "blocked" };
					}
				}
				const previouslyUnconfirmed = get().unconfirmedCreation;
				const previousContent = unconfirmedContent;
				// Recorded before the save, which marks the saved draft unconfirmed
				// only while its content is what this start sends.
				unconfirmedContent = draftContent();
				withoutSaving(() => set({ unconfirmedCreation: true }));
				if (!saveDraft()) {
					unconfirmedContent = previousContent;
					withoutSaving(() => set({ unconfirmedCreation: previouslyUnconfirmed }));
					return { status: "blocked" };
				}
				startDispatched = true;
				creationRequested = true;
				const result = await current.start({
					source,
					cwd: cwd.trim(),
					...(input.length ? { input } : {}),
					...(scalars.model ? { model: scalars.model } : {}),
					...(scalars.modelProvider ? { modelProvider: scalars.modelProvider } : {}),
					...(scalars.reasoningEffort ? { reasoningEffort: scalars.reasoningEffort } : {}),
					...(Object.keys(launchOverrides).length ? { launchOverrides } : {}),
				});
				if (generation !== connection) return { status: "obsolete" };
				if (storage)
					withoutSaving(() => {
						try {
							// This store is the hub's one writer and refuses edits while its
							// start is out, so the stored draft is the one this start sent,
							// unless saving the host's fill-ins on the way failed. Then the
							// draft stays as it is, still held, rather than be cleared
							// unmatched or started twice.
							const stored = storage().read(hubId);
							if (stored !== null && creationDraftMetadata({ ...stored, unconfirmed: false }) !== unconfirmedContent) {
								set({ error: CREATED_NOT_CLEARED });
								return;
							}
							storage().clear(hubId);
							emptyForm();
							lastSaved = creationDraftMetadata(snapshot());
						} catch {
							set({ error: CREATED_NOT_CLEARED });
						}
					});
				return { status: "created", hubId, thread: result.thread };
			} catch (error) {
				if (generation !== connection) return { status: "obsolete" };
				// The hub says when it refused a start before any session existed
				// (#3184): the draft isn't one that may have started, so Start stays.
				if (
					startDispatched &&
					error instanceof WireError &&
					mutationErrorData(error)?.mutationOutcome === "notAccepted"
				) {
					unconfirmedContent = null;
					set({
						unconfirmedCreation: false,
						error: `${error.message}\n\nNo session was started. Your input is kept.`,
					});
					return { status: "failed" };
				}
				// Any other failure of a start the hub was sent leaves it uncertain:
				// the hub can refuse one after its session exists (a refusal of the
				// initial input comes after the spawn), and an older hub never says.
				set({
					error: startDispatched
						? (error instanceof WireError ? `${error.message}\n\n` : "") +
							`Creation failed or could not be confirmed. Your input is kept. ${MAY_HAVE_STARTED}`
						: "Couldn't check the selected plugins, so no session was started. Your selection is kept.",
				});
				return { status: "failed" };
			} finally {
				if (generation === connection) set({ submitting: false });
			}
		},
		async changeHost(host, hostLabel) {
			const state = get();
			if (state.submitting || host === state.source) return;
			const current = service;
			const mine = movePlacement();
			const move = hostMoves;
			loadedContext = null;
			catalog++;
			refreshingModels = false;
			set({
				source: host,
				hostNote: null,
				movingHost: !!current,
				projects: [],
				models: [],
				recentModels: [],
				loadingModels: false,
			});
			if (!current) return;
			const cwd = state.cwd.trim();
			// A host that can't answer keeps the project: unknown isn't absent.
			const [exists, recent] = await Promise.all([
				cwd ? current.directoryExists(host, cwd).catch(() => null) : Promise.resolve(null),
				current.recentProjects(host).catch(() => [] as string[]),
			]);
			if (service !== current) return;
			set({
				...(move === hostMoves ? { movingHost: false } : {}),
				...(get().source === host ? { projects: recent } : {}),
			});
			// The form moved on while this host answered (a project chosen, a
			// latest start, another host): its choice stands.
			if (mine !== placement) return;
			const moved = moveToHost(cwd, hostLabel, exists !== false, recent);
			set({ cwd: moved.cwd, hostNote: moved.note });
			await get().loadModels();
		},
		applySetup(setup) {
			const previous = get();
			if (previous.submitting) return;
			movePlacement();
			set({
				source: setup.host,
				// Another host's recent projects are read below; the old host's go.
				...(setup.host !== previous.source ? { projects: [] } : {}),
				cwd: setup.cwd,
				hostNote: null,
				model: setup.model ? { provider: setup.model.provider, model: setup.model.model } : null,
				reasoning: setup.effort,
				launchOverrides: withOwnedOverrides(withoutModelChoice(previous.launchOverrides), setup.overrides),
			});
			if (setup.host !== previous.source) void get().loadMetadata();
			void get().loadModels(true);
		},
		applySeed(seed) {
			const previous = get();
			if (previous.submitting) return;
			movePlacement();
			pendingModelId = seed.model ?? null;
			set({
				source: seed.host,
				...(seed.host !== previous.source ? { projects: [] } : {}),
				cwd: seed.cwd,
				hostNote: null,
				model: null,
				reasoning: seed.effort ?? "",
				launchOverrides: withoutModelChoice(previous.launchOverrides),
			});
			if (seed.host !== previous.source) void get().loadMetadata();
			void get().loadModels(true);
		},
		retired: false,
		retire() {
			// Retired first, so unbinding persists nothing: the hub's drafts are
			// already gone.
			set({ retired: true });
			get().bind(null);
		},
		startMayRepeat() {
			return get().unconfirmedCreation && isUnconfirmedDraft();
		},
		discard() {
			if (get().submitting) return;
			if (!storage) {
				emptyForm();
				return;
			}
			try {
				withoutSaving(() => {
					storage().clear(hubId);
					emptyForm();
					lastSaved = creationDraftMetadata(snapshot());
				});
			} catch {
				// The empty form saves over the draft instead, or says it couldn't.
				emptyForm();
			}
		},
	}));
	/** A form with nothing in it: after a start, or when its draft is discarded. */
	function emptyForm(): void {
		movePlacement();
		unconfirmedContent = null;
		store.setState({
			source: LOCAL_HOST,
			hostNote: null,
			cwd: "",
			prompt: "",
			images: [],
			model: null,
			reasoning: "",
			launchOverrides: {},
			unconfirmedCreation: false,
			storageError: null,
			error: null,
		});
	}
	/** The draft as the person made it, whatever its unconfirmed flag says. */
	function draftContent(): string {
		return creationDraftMetadata(draftFields());
	}
	/** The draft as saved. It says a start may exist only while its content is
	 * still what that start sent: once edited, it is a new draft, and a reopened
	 * app mustn't hold it as the one that may have started. */
	function snapshot(): CreationDraft {
		const draft = draftFields();
		return {
			...draft,
			unconfirmed: store.getState().unconfirmedCreation && isUnconfirmedDraft(creationDraftMetadata(draft)),
		};
	}
	/** Whether the draft (`content`, the form's by default) is still what a
	 * start that couldn't be confirmed sent. With no such start, it isn't. */
	function isUnconfirmedDraft(content = draftContent()): boolean {
		if (unconfirmedContent === null) return false;
		return content === unconfirmedContent;
	}
	function draftFields(): CreationDraft {
		const state = store.getState();
		return {
			source: state.source,
			cwd: state.cwd,
			prompt: state.prompt,
			// The draft keeps its stored shape; the phone chooses no harness (ruling 13).
			harness: "",
			model: state.model,
			reasoning: state.reasoning,
			launchOverrides: state.launchOverrides,
			images: state.images,
			unconfirmed: false,
		};
	}
	function saveDraft(): boolean {
		// A removed hub's store writes nothing back.
		if (!storage || store.getState().retired) return true;
		if (!store.getState().storageLoaded) return false;
		const draft = snapshot();
		const signature = creationDraftMetadata(draft);
		if (signature === lastSaved && !store.getState().storageError) return true;
		return withoutSaving(() => {
			try {
				storage().write(hubId, draft);
				lastSaved = signature;
				store.setState({ storageError: null });
				return true;
			} catch {
				store.setState({
					storageError:
						"Changes could not be saved on this device. Keep this form open and retry saving before creating a session.",
				});
				return false;
			}
		});
	}
	function restoreDraft(): void {
		if (!storage) return;
		withoutSaving(() => {
			try {
				const draft = storage().read(hubId);
				if (draft) {
					const { unconfirmed, source, harness: _harness, launchOverrides, ...fields } = draft;
					// A per-launch model or effort, which only an older build could set,
					// becomes the form's own choice, as a session's does (applySeed): the
					// host's list finds the model, or the form says Hub default and sends
					// none, so the form never shows one model while sending another.
					const savedModel = launchOverrides.model?.trim();
					const savedEffort = launchOverrides.reasoningEffort?.trim();
					if (savedModel) pendingModelId = savedModel;
					store.setState({
						...fields,
						...(savedModel ? { model: null } : {}),
						...(savedEffort ? { reasoning: savedEffort } : {}),
						launchOverrides: withoutModelChoice(launchOverrides),
						// A draft saved before hosts has none: the hub's own machine
						// (ruling 28).
						source: source || LOCAL_HOST,
						unconfirmedCreation: unconfirmed,
						error: unconfirmed ? EARLIER_START_UNCONFIRMED : null,
					});
				}
				if (draft?.unconfirmed) unconfirmedContent = draftContent();
				store.setState({ storageLoaded: true, storageError: null });
				lastSaved = creationDraftMetadata(snapshot());
			} catch {
				store.setState({
					storageLoaded: false,
					storageError:
						"The saved creation draft could not be loaded. Retry loading it before editing or creating a session.",
				});
			}
		});
	}
	if (storage) {
		restoreDraft();
		store.subscribe(() => {
			if (!saving && store.getState().storageLoaded && creationDraftMetadata(snapshot()) !== lastSaved) saveDraft();
		});
	}
	return store;
}

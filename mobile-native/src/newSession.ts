import { createStore } from "zustand/vanilla";
import {
	buildComposerInput,
	harnessSupportsPluginSelection,
	MAX_ATTACHMENTS,
	markerText,
	pluginSelectionFromOverrides,
	pluginSelectionIssues,
	resolveScalars,
	stripMarker,
	WireError,
	withPluginSelection,
} from "@evener/appwire-client";
import type { HarnessDescriptor, LaunchConfigLayer, ModelDescriptor, Thread } from "@evener/appwire-client";
import { LOCAL_HOST } from "../../cmd/evener-hub/frontend/src/stores/hostRouting";
import type { NewSessionService } from "../../mobile/src/services/newSession";
import { type CreationDraft, type CreationDraftRepository, creationDraftMetadata } from "./creationDraftRepository";
import { type DraftImageData, imageInput } from "./draftImages";
import {
	type LaunchSetup,
	modelFromId,
	moveToHost,
	type SessionSeed,
	withOwnedOverrides,
} from "./newSession/launchSetup";

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
	harness: string;
	model: ModelDescriptor | null;
	reasoning: string;
	launchOverrides: LaunchConfigLayer;
	setLaunchOverrides(value: LaunchConfigLayer): void;
	projects: string[];
	harnesses: HarnessDescriptor[];
	models: ModelDescriptor[];
	loadingModels: boolean;
	submitting: boolean;
	error: string | null;
	metadataError: string | null;
	modelError: string | null;
	bind(service: NewSessionService | null): void;
	setCwd(value: string, refresh?: boolean): Promise<void>;
	setHarness(value: string): Promise<void>;
	setPrompt(value: string): void;
	selectModel(value: ModelDescriptor | null): void;
	setReasoning(value: string): void;
	loadMetadata(): Promise<void>;
	loadModels(refresh?: boolean): Promise<void>;
	submit(): Promise<Outcome>;
	changeHost(host: string, hostLabel: string): Promise<void>;
	applySetup(setup: LaunchSetup): void;
	applySeed(seed: SessionSeed): void;
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

export function createNewSessionStore(
	hubId: string,
	storage?: () => Pick<CreationDraftRepository, "read" | "write" | "clear">,
) {
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
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		setLaunchOverrides(value) {
			if (!get().submitting) set({ launchOverrides: JSON.parse(JSON.stringify(value)) });
		},
		projects: [],
		harnesses: [],
		models: [],
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
				harnesses: [],
				models: [],
				loadingModels: false,
				submitting: false,
				...(uncertainCreation
					? {
							error:
								"Creation could not be confirmed. Check the session list before trying again; the session may exist.",
						}
					: {}),
			});
		},
		async setCwd(cwd, refresh = true) {
			if (cwd.trim() === get().cwd.trim()) {
				set({ cwd });
				if (refresh) await get().loadModels();
				return;
			}
			movePlacement();
			loadedContext = null;
			catalog++;
			refreshingModels = false;
			set({
				cwd,
				models: [],
				model: null,
				reasoning: "",
				loadingModels: false,
			});
			if (refresh) await get().loadModels();
		},
		async setHarness(harness) {
			if (get().submitting) return;
			pendingModelId = null;
			set({
				...(harness !== get().harness ? { model: null, reasoning: "" } : {}),
				harness,
				launchOverrides: harnessSupportsPluginSelection(harness, get().harnesses)
					? get().launchOverrides
					: withPluginSelection(get().launchOverrides, { mode: "default" }),
			});
			await get().loadModels();
		},
		setPrompt(prompt) {
			set({ prompt });
		},
		selectModel(value) {
			const model = get().models.find((m) => m.provider === value?.provider && m.model === value.model) ?? null;
			const launchOverrides = { ...get().launchOverrides };
			const reasoning = launchOverrides.reasoningEffort || get().reasoning;
			delete launchOverrides.model;
			delete launchOverrides.reasoningEffort;
			set({
				model,
				launchOverrides,
				reasoning: model?.reasoningEffortLevels?.includes(reasoning) ? reasoning : "",
			});
		},
		setReasoning(value) {
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
				const [projects, harnesses] = await Promise.all([current.recentProjects(host), current.harnesses(host)]);
				if (generation === connection)
					set({
						// Another host's recent projects never land in this one's list.
						...(get().source === host ? { projects } : {}),
						harnesses,
						metadataError: null,
					});
			} catch {
				if (generation === connection)
					set({
						metadataError: "Could not load projects and harnesses. Retry options or use hub defaults.",
					});
			}
		},
		async loadModels(refresh = false) {
			const current = service;
			const { cwd, harness, source } = get();
			const context = JSON.stringify([source, cwd.trim(), harness]);
			if (current && loadedContext === context && !refresh) return;
			const selection = get().model;
			const reasoning = get().reasoning;
			loadedContext = null;
			const generation = ++catalog;
			refreshingModels = !!current && refresh;
			set({
				models: [],
				loadingModels: !!current,
			});
			if (!current) return;
			try {
				const result = await current.models(
					{
						...(cwd.trim() ? { cwd: cwd.trim() } : {}),
						...(harness ? { harness } : {}),
					},
					source,
				);
				if (generation === catalog) {
					loadedContext = context;
					const seeded = pendingModelId === null ? null : modelFromId(pendingModelId, result.data);
					pendingModelId = null;
					const model =
						seeded ??
						result.data.find((item) => item.provider === selection?.provider && item.model === selection.model) ??
						null;
					const settingsModel = creationModel(result.data, model, get().launchOverrides);
					set({
						models: result.data,
						model,
						reasoning: settingsModel?.reasoningEffortLevels?.includes(reasoning) ? reasoning : "",
						modelError: null,
					});
				}
			} catch {
				if (generation === catalog)
					set({
						modelError: "Could not load models. Retry options or use the hub default.",
					});
			} finally {
				if (generation === catalog) {
					refreshingModels = false;
					set({ loadingModels: false });
				}
			}
		},
		async submit() {
			const current = service;
			const generation = connection;
			const { source, cwd, prompt, harness, model, reasoning, submitting } = get();
			if (
				!current ||
				submitting ||
				get().movingHost ||
				refreshingModels ||
				!cwd.trim() ||
				!get().storageLoaded ||
				(model !== null && loadedContext !== JSON.stringify([source, cwd.trim(), harness]))
			)
				return { status: "blocked" };
			const launchOverrides = harnessSupportsPluginSelection(harness, get().harnesses)
				? get().launchOverrides
				: withPluginSelection(get().launchOverrides, { mode: "default" });
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
				saving = true;
				set({ unconfirmedCreation: true });
				saving = false;
				if (!saveDraft()) {
					saving = true;
					set({ unconfirmedCreation: previouslyUnconfirmed });
					saving = false;
					return { status: "blocked" };
				}
				startDispatched = true;
				creationRequested = true;
				const result = await current.start({
					source,
					cwd: cwd.trim(),
					...(input.length ? { input } : {}),
					...(harness ? { harness } : {}),
					...(scalars.model ? { model: scalars.model } : {}),
					...(scalars.modelProvider ? { modelProvider: scalars.modelProvider } : {}),
					...(scalars.reasoningEffort ? { reasoningEffort: scalars.reasoningEffort } : {}),
					...(Object.keys(launchOverrides).length ? { launchOverrides } : {}),
				});
				if (generation !== connection) return { status: "obsolete" };
				if (storage) {
					saving = true;
					try {
						storage().clear(hubId);
						movePlacement();
						set({
							source: LOCAL_HOST,
							hostNote: null,
							cwd: "",
							prompt: "",
							images: [],
							harness: "",
							model: null,
							reasoning: "",
							launchOverrides: {},
							unconfirmedCreation: false,
							storageError: null,
						});
						lastSaved = creationDraftMetadata(snapshot());
					} catch {
						set({
							storageError:
								"The session was created, but its local draft could not be cleared. Check the session list before reusing this draft.",
						});
					} finally {
						saving = false;
					}
				}
				return { status: "created", hubId, thread: result.thread };
			} catch (error) {
				if (generation !== connection) return { status: "obsolete" };
				set({
					error: startDispatched
						? (error instanceof WireError ? `${error.message}\n\n` : "") +
							"Creation failed or could not be confirmed. Your input is kept. Check the session list before trying again; the session may exist."
						: "Could not validate selected plugins. No session was requested. Reconnect or retry; your selection is kept.",
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
				launchOverrides: withOwnedOverrides(previous.launchOverrides, setup.overrides),
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
			});
			if (seed.host !== previous.source) void get().loadMetadata();
			void get().loadModels(true);
		},
	}));
	function snapshot(): CreationDraft {
		const state = store.getState();
		return {
			source: state.source,
			cwd: state.cwd,
			prompt: state.prompt,
			harness: state.harness,
			model: state.model,
			reasoning: state.reasoning,
			launchOverrides: state.launchOverrides,
			images: state.images,
			unconfirmed: state.unconfirmedCreation,
		};
	}
	function saveDraft(): boolean {
		if (!storage) return true;
		if (!store.getState().storageLoaded) return false;
		const draft = snapshot();
		const signature = creationDraftMetadata(draft);
		if (signature === lastSaved && !store.getState().storageError) return true;
		saving = true;
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
		} finally {
			saving = false;
		}
	}
	function restoreDraft(): void {
		if (!storage) return;
		saving = true;
		try {
			const draft = storage().read(hubId);
			if (draft) {
				const { unconfirmed, source, ...fields } = draft;
				store.setState({
					...fields,
					// A draft saved before hosts has none: the hub's own machine
					// (ruling 28).
					source: source ?? LOCAL_HOST,
					unconfirmedCreation: unconfirmed,
					error: unconfirmed
						? "An earlier creation could not be confirmed. Check the session list before trying again; the session may exist."
						: null,
				});
			}
			store.setState({ storageLoaded: true, storageError: null });
			lastSaved = creationDraftMetadata(snapshot());
		} catch {
			store.setState({
				storageLoaded: false,
				storageError:
					"The saved creation draft could not be loaded. Retry loading it before editing or creating a session.",
			});
		} finally {
			saving = false;
		}
	}
	if (storage) {
		restoreDraft();
		store.subscribe(() => {
			if (!saving && store.getState().storageLoaded && creationDraftMetadata(snapshot()) !== lastSaved) saveDraft();
		});
	}
	return store;
}

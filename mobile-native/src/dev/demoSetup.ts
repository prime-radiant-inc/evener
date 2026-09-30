// The demo hub's answers for New session and the Hub (phase 5's Task 26), from
// the prototype's data.js (docs/design/mobile/redesign/prototype/), which is
// the spec's canonical fixture (Appendix B): the hosts, providers, plugins,
// marketplaces, models and folders that Appendix A's frames 20-24 show. Every
// answer is typed as its method's result, so npm run check holds this fixture
// to the wire. Dev support only: nothing in the app imports it.
import { isDeepStrictEqual } from "node:util";
import { WireError } from "@evener/appwire-client";
import type {
	AuthStatusResponse,
	ContentLevel,
	HostRow,
	InstanceEntry,
	InstanceModelEntry,
	LaunchConfigLayer,
	MarketplaceEntry,
	MethodTypes,
	ModelDescriptor,
	ModelListResponse,
	PluginLaunchCandidate,
	TranscriptDisplayConfig,
	TranscriptDisplayDefaults,
} from "@evener/appwire-client";
import { HOST_DEPENDENT_DISCOVERY_METHODS } from "../../../cmd/evener-hub/frontend/src/stores/hostRouting";
import { type DemoFleet, EXPIRED_PROVIDER, PLUGINS, PROJECT_META } from "./demoFleet.js";

const HUB_VERSION = "0.9.412";

/** Every demo hub, fleet or not, is up to date, so the Hub's About reads as
 * it does on a real hub. */
export function demoUpdateCheck(): MethodTypes["evener/update/check"]["result"] {
	return {
		channel: "release",
		buildChannel: "release",
		currentVersion: HUB_VERSION,
		currentCommit: "demo",
		updateAvailable: false,
		applicable: true,
	};
}
// appwire/errors.go CodeConflict.
const CODE_CONFLICT = -32013;
const HOST = "paradise-park";
const HOST_VERSION = "0.9.409";
const OFFLINE_ERROR = "ssh: connect to host paradise-park port 22: Operation timed out";

// data.js's `providers`. `auth` is the transport's auth scheme as the hub names
// it (llm/registry/types.go): a key (bearer), a Codex account, Google's
// application default credentials, or nothing (ollama).
type AuthScheme = "bearer" | "oauth-openai-codex" | "gcp-adc" | "none";
// `email`: the account an account sign-in uses, as evener/auth/list names it.
const PROVIDERS: { id: string; base: string; auth: AuthScheme; models: string[]; email?: string }[] = [
	{
		id: "lunaroute",
		base: "openai-compatible",
		auth: "bearer",
		models: ["deepseek-4.1-flash", "glm-5.3-vision", "glm-5.2-vision", "glm-5.3-flash", "glm-5.3"],
	},
	{
		id: EXPIRED_PROVIDER,
		base: "codex",
		auth: "oauth-openai-codex",
		models: ["gpt-5.6", "gpt-5.6-luna", "gpt-6-astra"],
	},
	{
		id: "codex-jesse-at-pr",
		base: "codex",
		auth: "oauth-openai-codex",
		models: ["gpt-5.6"],
		email: "jesse@example.com",
	},
	{ id: "oai-jrv", base: "openai", auth: "bearer", models: ["gpt-5.5", "codex-auto-review"] },
	{ id: "meta", base: "meta", auth: "bearer", models: ["muse-spark-1.3"] },
	{ id: "kimi-code", base: "moonshot", auth: "bearer", models: ["k3"] },
	{ id: "openrouter-corp", base: "openrouter", auth: "bearer", models: ["claude-sonnet-5", "qwen3-coder-plus"] },
	{ id: "vertex", base: "vertex", auth: "gcp-adc", models: ["gemini-3-pro"] },
	{ id: "ollama", base: "ollama", auth: "none", models: ["qwen3-coder:30b"] },
	{ id: "groq3", base: "groq", auth: "bearer", models: ["kimi-k3-instant"] },
	{ id: "stepfun", base: "stepfun", auth: "bearer", models: ["step-3"] },
];

const ALL_EFFORTS = ["low", "medium", "high", "xhigh", "max"];
// data.js's `models`: id, name, provider, context in thousands, $ in and out
// per million tokens, vision, efforts.
const MODELS: [string, string, string, number, number, number, boolean, string[]][] = [
	["deepseek-4.1-flash", "DeepSeek 4.1 Flash", "lunaroute", 256, 0.3, 1.2, false, ALL_EFFORTS],
	["glm-5.3-vision", "GLM 5.3 Vision", "lunaroute", 200, 0.6, 2.2, true, ALL_EFFORTS],
	["glm-5.2-vision", "GLM 5.2 Vision", "lunaroute", 200, 0.5, 2.0, true, ALL_EFFORTS],
	["glm-5.3-flash", "GLM 5.3 Flash", "lunaroute", 128, 0.1, 0.4, false, ["low", "medium", "high"]],
	["glm-5.3", "GLM 5.3", "lunaroute", 200, 0.6, 2.2, false, ALL_EFFORTS],
	["gpt-5.6", "GPT-5.6", EXPIRED_PROVIDER, 400, 1.25, 10, true, ALL_EFFORTS],
	["gpt-5.6-luna", "GPT-5.6 Luna", EXPIRED_PROVIDER, 400, 1.25, 10, true, ALL_EFFORTS],
	["gpt-6-astra", "GPT-6 Astra", EXPIRED_PROVIDER, 1000, 5, 30, true, ALL_EFFORTS],
	["gpt-5.5", "GPT-5.5", "oai-jrv", 400, 1.25, 10, true, ALL_EFFORTS],
	["muse-spark-1.3", "Muse Spark 1.3", "meta", 1000, 2, 8, true, ["low", "medium", "high", "xhigh"]],
	["k3", "Kimi K3", "kimi-code", 256, 0.6, 2.5, false, ["low", "medium", "high", "xhigh"]],
	["claude-sonnet-5", "Claude Sonnet 5", "openrouter-corp", 1000, 3, 15, true, ALL_EFFORTS],
	["qwen3-coder-plus", "Qwen3 Coder Plus", "openrouter-corp", 256, 0.4, 1.6, false, ["low", "medium", "high"]],
	["gemini-3-pro", "Gemini 3 Pro", "vertex", 1000, 2, 12, true, ALL_EFFORTS],
	["qwen3-coder:30b", "Qwen3 Coder 30B (local)", "ollama", 128, 0, 0, false, ["low", "medium", "high"]],
];
const RECENT_MODELS = ["deepseek-4.1-flash", "glm-5.3-vision", "gpt-5.6"];
/** The provider instance each catalog model runs on, for the fleet's sign-in
 * notice count (DemoFleetOptions.modelProviders). */
export const DEMO_MODEL_PROVIDERS: Readonly<Record<string, string>> = Object.fromEntries(
	MODELS.map(([model, , provider]) => [model, provider]),
);
/** Each catalog model's display name, which a fleet row carries as its
 * model_name (S17, DemoFleetOptions.modelNames). */
export const DEMO_MODEL_NAMES: Readonly<Record<string, string>> = Object.fromEntries(
	MODELS.map(([model, displayName]) => [model, displayName]),
);
// What a provider's "Check for new models" finds that its listing lacks.
const FOUND_ON_CHECK: Record<string, string[]> = { lunaroute: ["glm-5.4"] };

function modelById(id: string): (typeof MODELS)[number] {
	const found = MODELS.find(([model]) => model === id);
	if (!found) throw new Error(`demoSetup: no model ${id}`);
	return found;
}

// data.js's `marketplaces`: a GitHub repo each, and one local folder.
const MARKETPLACES: { id: string; source: MarketplaceEntry["source"] }[] = [
	{ id: "superpowers-marketplace", source: { kind: "github", repo: "obra/superpowers-marketplace" } },
	{ id: "claude-plugins-official", source: { kind: "github", repo: "anthropics/claude-plugins-official" } },
	{ id: "go-skills", source: { kind: "github", repo: "prime-radiant-inc/go-skills" } },
	{ id: "prime-radiant-marketplace", source: { kind: "github", repo: "prime-radiant-inc/marketplace" } },
	{ id: "simplify-code-dev", source: { kind: "directory", path: "/home/jesse/git/simplify-code" } },
];

// What PLUGINS (demoFleet.ts) leaves out: data.js's description and counts
// text for each plugin, keyed by its id.
const PLUGIN_FACTS: Record<string, { description: string; counts: string }> = {
	superpowers: {
		description: "Brainstorming, TDD, debugging, plans and reviews",
		counts: "38 skills · 3 agents · 6 commands · 2 hooks",
	},
	"elements-of-style": { description: "Write clearly and concisely", counts: "1 skill" },
	"claude-session-driver": { description: "Manage other agent sessions as workers", counts: "1 skill · 1 command" },
	"private-journal-mcp": { description: "A private journal the agent can write to", counts: "1 MCP server" },
	"superpowers-chrome": { description: "Drive Chrome through DevTools", counts: "1 skill · 1 MCP server" },
	"frontend-design": { description: "Distinctive, production-grade interfaces", counts: "1 skill" },
	go: { description: "Idiomatic Go, testing and profiling", counts: "12 skills" },
	"go-release": { description: "Release engineering for Go modules", counts: "3 skills · 1 command" },
	"go-spec-reviewer": { description: "Review Go design specs", counts: "1 agent" },
	"fileflow-pathologize": { description: "Trace how files flow through a codebase", counts: "2 skills" },
	"iterative-development": { description: "Small steps with checkpoints", counts: "4 skills" },
	"shepherd-pr": { description: "Shepherd a PR through review to merge", counts: "2 skills · 2 commands · 1 hook" },
	"study-skills": { description: "Research and summarize unfamiliar code", counts: "3 skills" },
	"simplify-code": { description: "Simplify recently changed code", counts: "1 skill · 1 command" },
};

type Counts = Pick<PluginLaunchCandidate, "skillCount" | "agentCount" | "commandCount" | "hookCount" | "mcpCount">;
const COUNT_FIELDS: Record<string, keyof Counts> = {
	skill: "skillCount",
	agent: "agentCount",
	command: "commandCount",
	hook: "hookCount",
	"MCP server": "mcpCount",
};

/** "38 skills · 3 agents · 1 MCP server" as the preview's counts. */
function parseCounts(text: string): Counts {
	const counts: Counts = { skillCount: 0, agentCount: 0, commandCount: 0, hookCount: 0, mcpCount: 0 };
	for (const part of text.split(" · ")) {
		const match = /^(\d+) (.+?)s?$/.exec(part);
		const field = match ? COUNT_FIELDS[match[2] ?? ""] : undefined;
		if (!match || !field) throw new Error(`demoSetup: can't read the count "${part}"`);
		counts[field] = Number(match[1]);
	}
	return counts;
}

// Browse's catalogs: each marketplace offers its installed plugins, and
// go-skills three more to install.
const NOT_INSTALLED: Record<string, { name: string; description: string; category: string }[]> = {
	"go-skills": [
		{ name: "go-bench", description: "Benchmarks and profiles for Go", category: "development" },
		{ name: "go-lint-fix", description: "Fix what the linters report", category: "development" },
		{ name: "go-docs", description: "Write package documentation", category: "writing" },
	],
};

const LAUNCH_DEFAULTS: LaunchConfigLayer = {
	sandbox: "workspace-write",
	sandboxNet: true,
	contextStrategy: "compact",
	maxSubagentDepth: 2,
	maxRounds: -1,
};

/** One machine's folders, as the launch reads describe them. */
interface Machine {
	home: string;
	/** Every folder, without a trailing slash. */
	dirs: Set<string>;
	recent: string[];
	/** Folders that are git repositories, on main. */
	repos: string[];
	/** Chrome isn't installed here (superpowers-chrome's preview warning). */
	lacksChrome: boolean;
}

function machine(home: string, projects: string[], repos: string[], lacksChrome: boolean): Machine {
	const dirs = new Set<string>();
	for (const project of projects)
		for (let dir = project; dir !== ""; dir = dir.slice(0, dir.lastIndexOf("/"))) dirs.add(dir);
	return { home, dirs, recent: projects, repos, lacksChrome };
}

// The hub's own machine (magic-kingdom) holds the fleet's projects; its
// "home" project is the home folder itself, which isn't a repository.
const localProjects = PROJECT_META.map((project) => project.workingDir);
const PARADISE_PROJECTS = ["/Users/jesse/git/evener", "/Users/jesse/git/c-to-wasm"];

// The methods the hub forwards to another host for the spawn form, pinned to
// cmd/evener-hub/host_request_methods.txt by hostRouting's own test; the
// demo refuses anything else, as the hub's proxy does.
const FORWARDED: ReadonlySet<string> = new Set(HOST_DEPENDENT_DISCOVERY_METHODS);

type Answers = {
	[M in DemoSetupMethod]: (params: MethodTypes[M]["params"]) => MethodTypes[M]["result"];
};

const SETUP_METHODS = [
	"evener/host/list",
	"evener/host/attach",
	"evener/host/request",
	"evener/update/check",
	"evener/instance/list",
	"evener/instance/setModelDisabled",
	"evener/instance/refreshModels",
	"evener/auth/list",
	"evener/notices/list",
	"evener/auth/device/start",
	"evener/auth/device/poll",
	"evener/marketplace/list",
	"evener/marketplace/browse",
	"evener/plugin/preview",
	"model/list",
	"evener/projects/recent",
	"evener/harnesses/list",
	"evener/paths/complete",
	"evener/path/validate",
	"evener/dirs/create",
	"evener/git/head",
	"evener/launch/resolve",
	"evener/settings/transcriptDisplay/get",
	"evener/settings/transcriptDisplay/patch",
] as const;
export type DemoSetupMethod = (typeof SETUP_METHODS)[number];

export interface DemoSetup {
	/** Whether this fixture answers `method`; the demo hub answers the rest. */
	handles(method: string): method is DemoSetupMethod;
	answer<M extends DemoSetupMethod>(method: M, params: MethodTypes[M]["params"]): MethodTypes[M]["result"];
}

export function createDemoSetup(fleet: DemoFleet, options: { offlineHost?: boolean } = {}): DemoSetup {
	let hostAttached = !options.offlineHost;
	const local = machine(
		"/home/jesse",
		localProjects,
		localProjects.filter((dir) => dir !== "/home/jesse"),
		true,
	);
	const paradise = machine("/Users/jesse", PARADISE_PROJECTS, PARADISE_PROJECTS, false);

	function hostRow(): HostRow {
		return {
			name: HOST,
			origin: "hub.toml",
			address: "jesse@paradise-park",
			roots: ["/Users/jesse/git"],
			generation: 1,
			incarnationId: "demo-paradise-park",
			attached: hostAttached,
			midAttach: false,
			removed: false,
			hubVersion: HOST_VERSION,
			os: "darwin",
			arch: "arm64",
			...(hostAttached ? {} : { lastAttachError: OFFLINE_ERROR }),
		};
	}

	/** The launch reads, answered for one machine. */
	function launchAnswers(on: Machine) {
		const within = (path: string, root: string) => path === root || path.startsWith(`${root}/`);
		return {
			"evener/projects/recent": () => ({ data: on.recent }),
			// The hub offers one harness (launchHarnessDescriptors, app_models.go).
			"evener/harnesses/list": () => ({ data: [{ id: "evener", label: "Evener" }] }),
			"evener/paths/complete": ({ prefix }: MethodTypes["evener/paths/complete"]["params"]) => {
				const full = prefix === "" ? `${on.home}/` : prefix;
				// "/ho" completes from the root, whose children's parent is "".
				const parent = full.slice(0, full.lastIndexOf("/"));
				const data = [...on.dirs]
					.filter((dir) => dir.slice(0, dir.lastIndexOf("/")) === parent && `${dir}/`.startsWith(full))
					.sort()
					.map((dir) => `${dir}/`);
				return { data };
			},
			"evener/path/validate": ({ path }: MethodTypes["evener/path/validate"]["params"]) => ({
				path,
				valid: on.dirs.has(path.replace(/\/+$/, "")),
			}),
			"evener/dirs/create": ({ path }: MethodTypes["evener/dirs/create"]["params"]) => {
				const dir = path.replace(/\/+$/, "");
				const created = !on.dirs.has(dir);
				on.dirs.add(dir);
				return { path: dir, created };
			},
			"evener/git/head": ({ cwd }: MethodTypes["evener/git/head"]["params"]) => ({
				head: on.repos.some((repo) => within(cwd, repo)) ? "main" : "",
			}),
			"evener/launch/resolve": () => ({ effective: { ...LAUNCH_DEFAULTS }, layers: {}, provenance: {} }),
			"model/list": () => DEMO_MODEL_LIST,
			"evener/plugin/preview": ({ launchOverrides }: MethodTypes["evener/plugin/preview"]["params"]) => {
				const chosen = launchOverrides?.enabledPlugins;
				return {
					plugins: PLUGINS.map((plugin) => {
						const facts = PLUGIN_FACTS[plugin.id];
						return {
							name: plugin.id,
							version: plugin.version,
							description: facts?.description,
							source: "installed",
							marketplace: plugin.mp,
							selected: chosen ? chosen.includes(plugin.id) : plugin.on,
							...parseCounts(facts?.counts ?? ""),
						};
					}),
					...(on.lacksChrome
						? { diagnostics: [{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" }] }
						: {}),
				};
			},
		} satisfies Partial<Answers>;
	}
	const localLaunch = launchAnswers(local);
	// The hub's transcript display defaults per layout, from its shipped ones
	// (appwire/transcript_display.go TranscriptDisplayShippedDefaults).
	const transcriptDisplay: TranscriptDisplayDefaults = {
		desktop: { revision: 0, config: shippedTranscriptDisplay("tools") },
		mobile: { revision: 0, config: shippedTranscriptDisplay("intent") },
	};
	// The models each provider has turned off, and the providers whose models
	// were checked; the hub keeps both in its own config.
	const disabledModels = new Set<string>();
	const checked = new Set<string>();
	const providerModels = (provider: (typeof PROVIDERS)[number]): InstanceModelEntry[] =>
		[...provider.models, ...(checked.has(provider.id) ? (FOUND_ON_CHECK[provider.id] ?? []) : [])].map((id) =>
			disabledModels.has(`${provider.id}/${id}`) ? { id, disabled: true } : { id },
		);
	const instanceList = () => ({
		instances: PROVIDERS.map((provider, index) => instanceEntry(provider, index, providerModels(provider))),
		availableProviders: [],
	});
	const deviceFlows = new Set<string>();
	const requireCodex = (name: string) => {
		if (!PROVIDERS.some((provider) => provider.id === name && provider.auth === "oauth-openai-codex"))
			throw new Error(`OAuth is not supported for instance "${name}"`);
	};
	// app_instances.go's refusal for a name it doesn't have.
	const requireInstance = (name: string) => {
		if (!PROVIDERS.some((provider) => provider.id === name)) throw new Error(`instance "${name}" not found`);
	};
	// What paradise-park answers through the hub: its own folders, and the
	// hub's providers.
	const paradiseForwards = { ...launchAnswers(paradise), "evener/instance/list": instanceList };

	const answers: Answers = {
		...localLaunch,
		"evener/host/list": () => ({ hosts: [hostRow()] }),
		"evener/host/attach": ({ host }) => {
			if (host !== HOST) throw new Error(`unknown host "${host}"`);
			hostAttached = true;
			return { attached: true, host, hubVersion: HOST_VERSION, os: "darwin", arch: "arm64" };
		},
		// The hub's own refusals (app_host_admin.go): an unknown host, a method
		// off the list, a host it isn't attached to.
		"evener/host/request": ({ host, method, params }) => {
			if (host !== HOST) throw new Error(`unknown host "${host}"`);
			if (!FORWARDED.has(method)) throw new Error(`method "${method}" is not a permitted remote admin method`);
			if (!hostAttached) throw new Error(`host "${host}" is not attached`);
			const forward = paradiseForwards[method as keyof typeof paradiseForwards] as
				| ((params: unknown) => unknown)
				| undefined;
			// The rest of the list (the launch schema, the slash catalog) is
			// answered by neither side of the demo.
			if (!forward) throw new Error("Method not implemented by demonstration server");
			return forward(params ?? {}) as MethodTypes["evener/host/request"]["result"];
		},
		"evener/update/check": demoUpdateCheck,
		// app_auth.go's DeviceStart and DevicePoll, for a Codex provider only
		// (requiresCodex). The demo's code never gets authorized: a flow it
		// started stays pending, and one it didn't is expired.
		"evener/auth/device/start": ({ provider }) => {
			requireCodex(provider);
			const flowId = `demo-flow-${deviceFlows.size + 1}`;
			deviceFlows.add(flowId);
			return {
				provider,
				flowId,
				userCode: "WDJB-MJHT",
				verificationUrl: "https://example.com/device",
				intervalSeconds: 5,
			};
		},
		"evener/auth/device/poll": ({ provider, flowId }) => {
			requireCodex(provider);
			return { state: deviceFlows.has(flowId) ? "pending" : "expired" };
		},
		"evener/settings/transcriptDisplay/get": () => structuredClone(transcriptDisplay),
		// hubcore's TranscriptDisplayStore.Patch: a patch must name the layout's
		// current revision (transcriptDisplayConflict otherwise), keeps the
		// revision when it changes nothing, and moves it on by one when it does.
		"evener/settings/transcriptDisplay/patch": ({ layout, expectedRevision, config }) => {
			if (layout !== "desktop" && layout !== "mobile") throw new Error(`invalid transcript display layout "${layout}"`);
			const current = transcriptDisplay[layout];
			if (current.revision !== expectedRevision)
				throw new WireError(
					`transcript display ${layout} revision conflict: expected ${expectedRevision}, current ${current.revision}`,
					CODE_CONFLICT,
					{ evenerErrorInfo: "conflict", layout, current: structuredClone(current) },
				);
			if (!isDeepStrictEqual(current.config, config))
				transcriptDisplay[layout] = { revision: current.revision + 1, config: structuredClone(config) };
			return { layout, ...structuredClone(transcriptDisplay[layout]) };
		},
		"evener/instance/list": instanceList,
		"evener/instance/setModelDisabled": ({ name, model, disabled }) => {
			requireInstance(name);
			if (disabled) disabledModels.add(`${name}/${model}`);
			else disabledModels.delete(`${name}/${model}`);
			return instanceList();
		},
		"evener/instance/refreshModels": ({ name }) => {
			requireInstance(name);
			checked.add(name);
			return instanceList();
		},
		// Extends the fleet's answer (the Board's expired sign-in) with the
		// other providers that sign in with an account.
		"evener/auth/list": () => {
			const fleetStatuses = fleet.answerAuthList().providers;
			const known = new Set(fleetStatuses.map((status) => status.provider));
			const signedIn: AuthStatusResponse[] = PROVIDERS.filter(
				(provider) => provider.auth === "oauth-openai-codex" && !known.has(provider.id),
			).map((provider) => ({
				provider: provider.id,
				supported: true,
				signedIn: true,
				activeSource: "oauth",
				authModes: ["oauth"],
				hasStoredOAuth: true,
				needsLogin: false,
				email: provider.email,
			}));
			return { providers: [...fleetStatuses, ...signedIn] };
		},
		"evener/notices/list": () => fleet.answerNoticesList(),
		"evener/marketplace/list": () => ({
			marketplaces: MARKETPLACES.map((marketplace) => ({
				name: marketplace.id,
				source: marketplace.source,
				lastUpdated: 1_790_000_000,
			})),
		}),
		"evener/marketplace/browse": ({ name }) => {
			// internal/plugins/errors.go's ErrMarketplaceNotFound.
			if (!MARKETPLACES.some((marketplace) => marketplace.id === name)) throw new Error("marketplace not found");
			const installed = PLUGINS.filter((plugin) => plugin.mp === name).map((plugin) => ({
				name: plugin.id,
				description: PLUGIN_FACTS[plugin.id]?.description,
			}));
			return { name, plugins: [...installed, ...(NOT_INSTALLED[name] ?? [])] };
		},
	};

	return {
		handles: (method): method is DemoSetupMethod => (SETUP_METHODS as readonly string[]).includes(method),
		answer: (method, params) => answers[method](params as never) as never,
	};
}

function modelDescriptor([
	model,
	displayName,
	provider,
	contextK,
	input,
	output,
	vision,
	efforts,
]: (typeof MODELS)[number]): ModelDescriptor {
	return {
		provider,
		model,
		displayName,
		contextWindow: contextK * 1000,
		inputCostPerMillion: input,
		outputCostPerMillion: output,
		supportsVision: vision,
		supportsTools: true,
		supportsReasoning: true,
		reasoningEffortLevels: efforts,
	};
}

// The model catalog the demo hub lists with EVENER_DEMO_FLEET, for New
// session's picker and a session's model sheet alike: data.js's models, with
// the recent ones first.
export const DEMO_MODEL_LIST: ModelListResponse = {
	data: MODELS.map(modelDescriptor),
	recent: RECENT_MODELS.map((id) => modelDescriptor(modelById(id))),
};

// Each scheme's sign-in facts as the hub reports them: authModes from
// app_auth.go's authModesFor, and the source a configured credential of that
// kind reports (a stored key, a Codex record, application default
// credentials, or none).
const SIGN_IN: Record<
	AuthScheme,
	Pick<InstanceEntry, "authModes" | "activeSource" | "hasStoredOAuth" | "hasStoredFile" | "credentialRequired">
> = {
	bearer: {
		authModes: ["apiKey"],
		activeSource: "store",
		hasStoredOAuth: false,
		hasStoredFile: true,
		credentialRequired: true,
	},
	"oauth-openai-codex": { authModes: ["oauth"], activeSource: "oauth", hasStoredOAuth: true, credentialRequired: true },
	"gcp-adc": {
		authModes: ["adc", "credentialJson"],
		activeSource: "adc",
		hasStoredOAuth: false,
		credentialRequired: true,
	},
	none: { authModes: ["none"], activeSource: "none", hasStoredOAuth: false, credentialRequired: false },
};

function shippedTranscriptDisplay(level: ContentLevel): TranscriptDisplayConfig {
	return {
		version: 1,
		content: { kind: "preset", level },
		advanced: {
			roundTimings: false,
			tokenCounts: false,
			estimatedCost: false,
			systemEvents: false,
			promptEvents: false,
			hookExits: "none",
		},
	};
}

function instanceEntry(
	provider: (typeof PROVIDERS)[number],
	index: number,
	models: InstanceModelEntry[],
): InstanceEntry {
	return {
		name: provider.id,
		providerId: provider.base,
		protocol: "openai",
		auth: provider.auth,
		implicit: false,
		isDefault: index === 0,
		models,
		...SIGN_IN[provider.auth],
		...(provider.email ? { storedEmail: provider.email } : {}),
	};
}

// A new session's setup (spec 11): where it runs and how. The sheet's rows,
// the latest start it opens on, and a host change all speak in this shape,
// and every rule applied to it is a pure function here.
import { basename, type LaunchConfigLayer } from "@evener/appwire-client";
import { effortName, hostIdOf } from "../session/sessionFacts";

/** The launch overrides the sheet owns: Plugins, Access and More options.
 * Everything else stays at the hub's defaults on the phone (spec 11). */
export const OWNED_FIELDS = [
	"enabledPlugins",
	"sandbox",
	"sandboxNet",
	"contextStrategy",
	"maxSubagentDepth",
	"maxRounds",
] as const satisfies readonly (keyof LaunchConfigLayer)[];

export type OwnedOverrides = Pick<LaunchConfigLayer, (typeof OWNED_FIELDS)[number]>;

export interface ModelChoice {
	provider: string;
	model: string;
}

export interface LaunchSetup {
	/** "local" is the hub's own machine; anything else is a host's name. */
	host: string;
	cwd: string;
	/** null is the hub's default model. */
	model: ModelChoice | null;
	/** "" is the model's own default effort. */
	effort: string;
	overrides: OwnedOverrides;
}

/** What "New session like this" copies from a session (ruling 24). */
export interface SessionSeed {
	host: string;
	cwd: string;
	/** The session's model as it reports it: "provider/model", or a bare model. */
	model?: string;
	effort?: string;
}

/** A session read as New session like this's seed (ruling 24): its host,
 * folder, and its own model and effort when it has them. The session's
 * plugins and access aren't on the wire, so the sheet keeps its own. */
export function seedFromSession(
	ref: string,
	session: { cwd: string; modelProvider: string; reasoningEffort?: string },
): SessionSeed {
	return {
		host: hostIdOf(ref),
		cwd: session.cwd,
		...(session.modelProvider ? { model: session.modelProvider } : {}),
		...(session.reasoningEffort ? { effort: session.reasoningEffort } : {}),
	};
}

export function ownedOverrides(layer: LaunchConfigLayer): OwnedOverrides {
	const owned: OwnedOverrides = {};
	if (layer.enabledPlugins !== undefined) owned.enabledPlugins = [...layer.enabledPlugins];
	if (layer.sandbox !== undefined) owned.sandbox = layer.sandbox;
	if (layer.sandboxNet !== undefined) owned.sandboxNet = layer.sandboxNet;
	if (layer.contextStrategy !== undefined) owned.contextStrategy = layer.contextStrategy;
	if (layer.maxSubagentDepth !== undefined) owned.maxSubagentDepth = layer.maxSubagentDepth;
	if (layer.maxRounds !== undefined) owned.maxRounds = layer.maxRounds;
	return owned;
}

/** `layer` with its owned fields replaced by `owned`'s. A field `owned` leaves
 * out is removed, so a setup with no sandbox falls back to the hub's. */
export function withOwnedOverrides(layer: LaunchConfigLayer, owned: OwnedOverrides): LaunchConfigLayer {
	const next: LaunchConfigLayer = { ...layer };
	for (const field of OWNED_FIELDS) delete next[field];
	return { ...next, ...ownedOverrides(owned) };
}

export interface HostMove {
	cwd: string;
	/** The line under the Host row saying what moved, or null. */
	note: string | null;
}

/** Where the project goes when the host changes (spec 11; ruling 17): it stays
 * when the new host has it, else moves to that host's most recent project and
 * says so. With no project chosen yet, it quietly takes that recent project. */
export function moveToHost(
	cwd: string,
	hostLabel: string,
	projectExists: boolean,
	recentOnHost: readonly string[],
): HostMove {
	const recent = recentOnHost[0];
	if (!cwd) return { cwd: recent ?? "", note: null };
	if (projectExists) return { cwd, note: null };
	const name = projectName(cwd);
	if (!recent) return { cwd: "", note: `${name} isn't on ${hostLabel}. Choose a project.` };
	// Two folders with one name (a repository cloned on both hosts) would read
	// "evener isn't on paradise-park, so the project changed to evener": the
	// shortest ends of their paths that differ tell them apart.
	const [from, to] = name === projectName(recent) ? distinctTails(cwd, recent) : [name, projectName(recent)];
	return { cwd: recent, note: `${from} isn't on ${hostLabel}, so the project changed to ${to}.` };
}

/** The shortest ends of two paths that differ ("work/evener" and
 * "oss/evener"); a path that runs out first is given whole. */
function distinctTails(a: string, b: string): [string, string] {
	const left = a.split("/").filter(Boolean);
	const right = b.split("/").filter(Boolean);
	// Compared by folder, so a path given whole never differs by its slash alone.
	const tail = (segments: string[], whole: string, count: number) =>
		count >= segments.length ? whole : segments.slice(-count).join("/");
	for (let count = 1; count <= Math.max(left.length, right.length); count++) {
		if (left.slice(-count).join("/") !== right.slice(-count).join("/"))
			return [tail(left, a, count), tail(right, b, count)];
	}
	return [a, b];
}

/** A project's name: its folder (spec 11's "Project evener"). */
export function projectName(cwd: string): string {
	return basename(cwd) || cwd;
}

/** An effort level's label on New session's Effort control, spelled as spec
 * 11's frame spells them: Low, Med, High, XHigh, Max. It is the composer
 * chip's name for the level (effortName), shortened where a segment needs it. */
export function effortLabel(level: string): string {
	return level === "medium" ? "Med" : effortName(level);
}

export interface AccessLevel {
	/** The launch override's `sandbox` value. */
	mode: string;
	label: string;
	detail: string;
}

const FULL_ACCESS: AccessLevel = { mode: "off", label: "Full access", detail: "No sandbox: reads and writes anywhere" };

/** Spec 11's four access levels, in the hub's sandbox terms (schema.go's
 * sandbox option). */
export const ACCESS_LEVELS: readonly AccessLevel[] = [
	FULL_ACCESS,
	{
		mode: "workspace-write",
		label: "Workspace write",
		detail: "Writes only in the project; reads anywhere but secrets",
	},
	{ mode: "read-only", label: "Read-only", detail: "Writes nothing; reads anywhere but secrets" },
	{ mode: "restricted", label: "Restricted", detail: "Reads and writes only in the project" },
];

/** The access a setup runs with: its own sandbox, else the hub's default for
 * the project (launch/resolve's effective value), else the schema's "off". A
 * mode this build doesn't know shows as the hub named it. */
export function accessOf(sandbox: string | undefined, hubDefault: string | undefined): AccessLevel {
	const mode = sandbox || hubDefault || FULL_ACCESS.mode;
	return ACCESS_LEVELS.find((level) => level.mode === mode) ?? { mode, label: mode, detail: "" };
}

/** The access a form can name: the person's own choice, else the hub's
 * default once the hub has said it (`hubDefaults` null until then). Null while
 * the default is unknown, since accessOf's fallback, no sandbox, is the least
 * safe guess. */
export function knownAccess(sandbox: string | undefined, hubDefaults: { sandbox?: string } | null): AccessLevel | null {
	return sandbox || hubDefaults ? accessOf(sandbox, hubDefaults?.sandbox) : null;
}

/** Network matters only inside a sandbox (the schema's sandbox_net: "Has no
 * effect unless a sandbox mode is set"), so its switch shows only then. */
export function networkApplies(access: AccessLevel): boolean {
	return access.mode !== FULL_ACCESS.mode;
}

/** The creation form's fields as a setup. */
export function setupOf(form: {
	source: string;
	cwd: string;
	model: ModelChoice | null;
	reasoning: string;
	launchOverrides: LaunchConfigLayer;
}): LaunchSetup {
	return {
		host: form.source,
		cwd: form.cwd.trim(),
		model: form.model ? { provider: form.model.provider, model: form.model.model } : null,
		effort: form.reasoning,
		overrides: ownedOverrides(form.launchOverrides),
	};
}

/** A model named the way a session or a launch override names one:
 * "provider/model", or a bare model only one provider offers. */
export function modelFromId<T extends ModelChoice>(id: string, models: readonly T[]): T | null {
	const matches = models.filter((model) => `${model.provider}/${model.model}` === id || model.model === id);
	return matches.length === 1 ? (matches[0] ?? null) : null;
}

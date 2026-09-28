// A new session's setup (spec 11): where it runs and how. The sheet's rows,
// the latest start it opens on, and a host change all speak in this shape,
// and every rule applied to it is a pure function here.
import { basename, type LaunchConfigLayer } from "@evener/appwire-client";
import { effortName } from "../session/sessionFacts";

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

/** A remembered start: its setup, and when a session started with it (ms). */
export interface RememberedSetup {
	setup: LaunchSetup;
	at: number;
}

/** What "New session like this" copies from a session (ruling 24). */
export interface SessionSeed {
	host: string;
	cwd: string;
	/** The session's model as it reports it: "provider/model", or a bare model. */
	model?: string;
	effort?: string;
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

/** The newest setup started from this phone: where a sheet with nothing in
 * it yet begins. */
export function newestSetup(history: readonly RememberedSetup[]): LaunchSetup | null {
	let newest: RememberedSetup | null = null;
	for (const entry of history) if (!newest || entry.at > newest.at) newest = entry;
	return newest?.setup ?? null;
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
	return { cwd: recent, note: `${name} isn't on ${hostLabel}, so the project changed to ${projectName(recent)}.` };
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

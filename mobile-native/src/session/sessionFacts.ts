// What the Session sheet shows (spec 8.6): only facts the thread carries
// (ruling 21). That is where it runs, its access, its plugins and its usage.
// The same file names the model for the composer's chip (spec 8.5).
import { type ModelDescriptor, sessionEffortLevels, type ThreadModel } from "@evener/appwire-client";
import { compactCount, compactDuration } from "./format";

/** A ref names its host first ("<host>:<session>"); "local" is the hub's own. */
export function hostIdOf(ref: string): string {
	const at = ref.indexOf(":");
	return at > 0 ? ref.slice(0, at) : "local";
}

export interface WhereFacts {
	host: string;
	project?: string;
	directory: string;
	branch?: string;
}

function basename(path: string): string {
	const trimmed = path.replace(/\/+$/, "");
	return trimmed.slice(trimmed.lastIndexOf("/") + 1);
}

export function whereFacts(
	session: Pick<ThreadModel, "ref" | "cwd" | "projectPath" | "gitBranch">,
	hostLabel: (hostId: string) => string,
): WhereFacts {
	const project = session.projectPath ? basename(session.projectPath) : "";
	return {
		host: hostLabel(hostIdOf(session.ref)),
		...(project ? { project } : {}),
		directory: session.cwd,
		...(session.gitBranch ? { branch: session.gitBranch } : {}),
	};
}

/** Plugins are fixed when a session starts. An absent inventory is unknown, not
 * empty (model.ts, ThreadDiagnostics). */
export function pluginsLine(session: Pick<ThreadModel, "diagnostics">): { line: string; names: string[] } | null {
	const plugins = session.diagnostics?.plugins;
	if (!plugins) return null;
	const names = plugins.map((plugin) => plugin.name).sort((a, b) => a.localeCompare(b));
	return { line: `${names.length} ${names.length === 1 ? "plugin" : "plugins"} · chosen at start`, names };
}

// The sandbox modes as the spec names them (8.6, and 7.x's new session).
const ACCESS_MODES: Record<string, string> = {
	off: "Full access",
	"workspace-write": "Workspace write",
	"read-only": "Read-only",
	restricted: "Restricted",
};

/** The session's sandbox mode and network setting (S15), or null when the hub
 * doesn't report them. A mode the phone doesn't know shows its raw name. */
export function accessFacts(session: Pick<ThreadModel, "access">): { mode: string; network: string } | null {
	const access = session.access;
	if (!access) return null;
	return { mode: ACCESS_MODES[access.sandbox] ?? access.sandbox, network: access.network ? "On" : "Off" };
}

export interface UsageFacts {
	tokens?: string;
	split?: string;
	cost?: string;
	workTime?: string;
	/** Used of the window. The hub reports no compaction threshold, so the
	 * gauge draws no marker (ruling 36). */
	context?: { text: string; fraction: number };
	failedToolCalls?: string;
}

export function usageFacts(
	session: Pick<ThreadModel, "usage" | "cost" | "workMillis" | "contextUsed" | "contextWindow" | "failedToolCalls">,
): UsageFacts {
	const facts: UsageFacts = {};
	const usage = session.usage;
	if (usage?.totalTokens) facts.tokens = `${compactCount(usage.totalTokens)} tokens`;
	const split = [
		usage?.inputTokens ? `${compactCount(usage.inputTokens)} in` : "",
		usage?.outputTokens ? `${compactCount(usage.outputTokens)} out` : "",
		usage?.cacheReadTokens ? `${compactCount(usage.cacheReadTokens)} cached` : "",
	].filter(Boolean);
	if (split.length > 0) facts.split = split.join(" · ");
	if (session.cost) facts.cost = session.cost;
	if (session.workMillis > 0) facts.workTime = compactDuration(session.workMillis);
	if (session.contextWindow > 0)
		facts.context = {
			text: `${compactCount(session.contextUsed)} of ${compactCount(session.contextWindow)}`,
			fraction: Math.min(1, Math.max(0, session.contextUsed / session.contextWindow)),
		};
	const failed = session.failedToolCalls ?? 0;
	if (failed > 0) facts.failedToolCalls = `${failed} failed tool ${failed === 1 ? "call" : "calls"}`;
	return facts;
}

// The ladder's names as people read them in a chip ("GLM 5.3 Vision · XHigh").
const EFFORT_NAMES: Record<string, string> = {
	"": "Default",
	none: "Off",
	minimal: "Minimal",
	low: "Low",
	medium: "Medium",
	high: "High",
	xhigh: "XHigh",
	max: "Max",
};

export function effortName(level: string): string {
	return EFFORT_NAMES[level] ?? `${level.charAt(0).toUpperCase()}${level.slice(1)}`;
}

/** The composer's chip: the model as the catalog names it (a model is called
 * one way everywhere people read it), then its effort when the model has
 * levels. Before the catalog loads, the model id stands in. */
export function modelChipLabel(
	session: Pick<ThreadModel, "modelProvider" | "reasoningEffort" | "reasoningEffortLevels" | "supportsReasoning">,
	catalog: readonly ModelDescriptor[] | undefined,
): string {
	const match = catalog?.find((entry) => `${entry.provider}/${entry.model}` === session.modelProvider);
	const name = match?.displayName || session.modelProvider.slice(session.modelProvider.indexOf("/") + 1) || "Model";
	const levels = sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning);
	return levels.length > 0 ? `${name} · ${effortName(session.reasoningEffort ?? "")}` : name;
}

// What the Session sheet shows (spec 8.6): only facts the thread carries
// (ruling 21). That is where it runs, its access, its plugins and its usage.
// The same file names the model for the composer's chip (spec 8.5).
import {
	basename,
	formatTokenCount,
	type ModelDescriptor,
	sessionEffortLevels,
	type ThreadModel,
} from "@evener/appwire-client";
import type { MobileTimelineItem } from "../projectedRows";
import { localSessionId } from "../sessionDeletionResult";
import { compactDuration } from "./format";

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

/** A host as the Session sheet shows it (spec 8.6): its name, and whether
 * it's connected, for its dot; null when nothing says. */
export interface SessionHost {
	label: string;
	online: boolean | null;
}

/** Names each host a session can run on. The hub's own machine goes by the
 * hub's name, as Hub > Hosts names it, where the manifest calls it "this
 * host", and is as connected as the hub; any other host goes by its manifest
 * label and state, or by its id while the manifest doesn't list it. While the
 * hub is away every host it lists reads offline, since the phone can't reach
 * any of them. */
export function sessionHosts(
	sources: readonly { id: string; label: string; online: boolean }[] | undefined,
	hubName: string | null,
	hubConnected: boolean,
): (hostId: string) => SessionHost {
	const byId = new Map((sources ?? []).map((source) => [source.id, source]));
	return (hostId) => {
		const source = byId.get(hostId);
		if (hostId === "local") return { label: hubName ?? source?.label ?? hostId, online: hubConnected };
		return { label: source?.label ?? hostId, online: source ? hubConnected && source.online : null };
	};
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
	if (usage?.totalTokens) facts.tokens = `${formatTokenCount(usage.totalTokens)} tokens`;
	const split = [
		usage?.inputTokens ? `${formatTokenCount(usage.inputTokens)} in` : "",
		usage?.outputTokens ? `${formatTokenCount(usage.outputTokens)} out` : "",
		usage?.cacheReadTokens ? `${formatTokenCount(usage.cacheReadTokens)} cached` : "",
	].filter(Boolean);
	if (split.length > 0) facts.split = split.join(" · ");
	if (session.cost) facts.cost = session.cost;
	if (session.workMillis > 0) facts.workTime = compactDuration(session.workMillis);
	if (session.contextWindow > 0)
		facts.context = {
			text: `${formatTokenCount(session.contextUsed)} of ${formatTokenCount(session.contextWindow)}`,
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

/** A model as the catalog names it (a model is called one way everywhere
 * people read it). While the catalog is away (before the first load, and
 * after a failed load clears it; a reload keeps it) the hub's session row names it (S17's model_name, the
 * same name model/list gives), and only then the id. Right after a switch the
 * row may lag until the fleet re-reads it; that only shows while the catalog
 * is away too. */
function modelName(modelProvider: string, catalog: readonly ModelDescriptor[] | undefined, rowName?: string): string {
	const match = catalog?.find((entry) => `${entry.provider}/${entry.model}` === modelProvider);
	return match?.displayName || rowName || modelProvider.slice(modelProvider.indexOf("/") + 1) || "Model";
}

/** The composer's chip: the model's name, then its effort when the model has
 * levels. */
export function modelChipLabel(
	session: Pick<ThreadModel, "modelProvider" | "reasoningEffort" | "reasoningEffortLevels" | "supportsReasoning">,
	catalog: readonly ModelDescriptor[] | undefined,
	/** The session's Board row's model_name, when the fleet has the row. */
	rowName?: string,
): string {
	const name = modelName(session.modelProvider, catalog, rowName);
	const levels = sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning);
	return levels.length > 0 ? `${name} · ${effortName(session.reasoningEffort ?? "")}` : name;
}

/** Whether the model sheet has something to change: the model, or its
 * effort. The composer's chip and the Session sheet's Model row both open it
 * on this, so Effort is reachable from either. */
export function canOpenModelSheet(
	session: Pick<ThreadModel, "capabilities" | "reasoningEffortLevels" | "supportsReasoning">,
): boolean {
	return (
		session.capabilities.changeModel ||
		sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning).length > 0
	);
}

/** The Session sheet's vision model row: "off" turns vision off, and empty
 * uses the session's own model. */
export function visionModelLabel(visionModel: string, catalog: readonly ModelDescriptor[] | undefined): string {
	if (visionModel === "off") return "Off";
	if (visionModel === "") return "Session model";
	return modelName(visionModel, catalog);
}

/** The Notes & links row: "Your note · agent note · 3 links", naming only the
 * parts present, or "None". */
export function notesSummary(session: Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls">): string {
	const count = session.sessionUrls.length;
	const parts = [
		session.humanNote.trim() ? "Your note" : "",
		session.agentNote.trim() ? "agent note" : "",
		count > 0 ? `${count} ${count === 1 ? "link" : "links"}` : "",
	].filter(Boolean);
	return parts.length > 0 ? parts.join(" · ") : "None";
}

/** Where "Fork from latest" forks: your latest message with a transcript
 * entry a fork can start from, by the rule a message's own "Fork from here"
 * follows (TimelineItem's YourMessage, the screen's forkMessage). */
export function latestForkPoint(items: readonly MobileTimelineItem[]): { entryIndex: number; preview: string } | null {
	for (let index = items.length - 1; index >= 0; index -= 1) {
		const item = items[index];
		if (
			item?.kind === "user" &&
			item.transcriptEntryIndex !== undefined &&
			Number.isSafeInteger(item.transcriptEntryIndex) &&
			item.transcriptEntryIndex > 0
		)
			return { entryIndex: item.transcriptEntryIndex, preview: item.text };
	}
	return null;
}

/** Whether Delete can remove the session's saved copy: a local session that
 * isn't loaded, which is what SessionDeletion deletes. */
export function canDeleteSavedSession(session: Pick<ThreadModel, "ref" | "status">): boolean {
	return localSessionId(session.ref) !== null && session.status.type === "notLoaded";
}

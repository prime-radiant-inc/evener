// What the Session sheet shows (spec 8.6): only facts the thread carries
// (ruling 21). That is where it runs, its access, its plugins and its usage.
// The same file names the model for the composer's chip (spec 8.5).
import {
	basename,
	compactDuration,
	effectiveContextStatus,
	formatTokenCount,
	hostIdOf,
	loadedPluginNames,
	sessionEffortLevels,
	type ModelDescriptor,
	type ThreadModel,
} from "@evener/appwire-client";
import type { MobileTimelineItem } from "../projectedRows";
import { sameModel } from "../subagents/subagentModel";
import { localSessionId } from "../sessionDeletionResult";

export interface WhereFacts {
	host: string;
	project?: string;
	directory: string;
	branch?: string;
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

/** The Plugins row: how many the session loaded, and their names. Null when
 * the inventory is unknown (loadedPluginNames). */
export function pluginsLine(session: Pick<ThreadModel, "diagnostics">): { line: string; names: string[] } | null {
	const names = loadedPluginNames(session);
	if (!names) return null;
	return { line: `${names.length} ${names.length === 1 ? "plugin" : "plugins"} · chosen at start`, names };
}

export interface UsageFacts {
	tokens?: string;
	split?: string;
	cost?: string;
	workTime?: string;
	/** Used of the active context ceiling (custom compactionThreshold when
	 * configured, otherwise contextWindow). */
	context?: { text: string; fraction: number };
	failedToolCalls?: string;
}

export function usageFacts(
	session: Pick<
		ThreadModel,
		"usage" | "cost" | "workMillis" | "contextUsed" | "contextWindow" | "contextAssumed" | "failedToolCalls"
	> & { compactionThreshold?: number },
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
	const status = effectiveContextStatus(session);
	if (status.ceiling > 0)
		facts.context = {
			// The "~" marks an ASSUMED window the way the cost row marks its
			// derived estimate ("~$…"): the used count stays plain because it
			// is measured; only the capacity is assumed. An explicit
			// compactionThreshold is never marked with "~".
			text: status.hasCustomThreshold
				? `${formatTokenCount(session.contextUsed)} of ${formatTokenCount(status.ceiling)}`
				: `${formatTokenCount(session.contextUsed)} of ${session.contextAssumed ? "~" : ""}${formatTokenCount(status.ceiling)}`,
			fraction: Math.min(1, Math.max(0, status.pressure)),
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
	const model = modelProvider.slice(modelProvider.indexOf("/") + 1);
	// The exact provider instance first. A gateway and its upstream can name
	// the same model under different provider keys, and the display name
	// belongs to the model, so the bare id still finds it (#2988).
	const match =
		catalog?.find((entry) => `${entry.provider}/${entry.model}` === modelProvider) ??
		catalog?.find((entry) => sameModel(entry.model, modelProvider));
	return match?.displayName || rowName || model || "Model";
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

import type {
	NavigationProjectCatalog,
	NavigationProjectSummary,
	NavigationSessionLocation,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { navigationReadback } from "./navigationReadback";
import { localSessionId } from "./sessionDeletionResult";

/** The ref a session archive's location is read under. This hub's session is
 * archived by its bare id and another host's by its ref (ruling 20, the web
 * rail's archiveSessionIdentity); anything else can't be checked. A ref's two
 * parts follow the hub's own rule (appwire/refs.go). */
function archivedSessionRef(id: string): string | null {
	if (localSessionId(`local:${id}`)) return `local:${id}`;
	return /^(?!local:)[A-Za-z0-9._~-]+:[A-Za-z0-9._~-]+$/.test(id) ? id : null;
}

export interface OrganizationObservation {
	generationId: string;
	title: string;
	state: "archived" | "current" | "recent" | "projects" | "pinned" | "unpinned";
	settled: boolean;
}

export async function readOrganizationNavigation(
	client: ConversationClientLike,
	checkpoint: NavigationActionCheckpoint,
	current: () => boolean,
	confirmReceipt = false,
): Promise<OrganizationObservation> {
	const operation = checkpoint.operation;
	if (operation.kind !== "archive" && operation.kind !== "favorite")
		throw Error("Return to the previous organization screen to check that change.");
	const { params } = operation;
	const sessionRef =
		operation.kind === "archive" && params.kind === "session" ? archivedSessionRef(params.id) : undefined;
	if (sessionRef === null) throw Error("This session's archive change can't be checked here.");
	const navigation = await navigationReadback(client, checkpoint.receipt, current, confirmReceipt);
	let title: string, state: OrganizationObservation["state"], matches: boolean;
	if (operation.kind === "archive" && sessionRef !== undefined) {
		const ref = sessionRef;
		const local = ref.startsWith("local:");
		const response = await navigation.read({ resource: "location", ref });
		const location = response.data as NavigationSessionLocation | null;
		if (
			!location ||
			location.ref !== ref ||
			location.session?.ref !== ref ||
			// This hub's row names its session by id; another host's row is
			// that host's, which its ref already names.
			(local
				? location.session.session_id !== params.id || location.session.host_id !== "local"
				: location.session.host_id === "local") ||
			!location.top_level ||
			!["current", "recent", "archived"].includes(location.tier ?? "")
		)
			throw Error("This session's current organization could not be confirmed.");
		title = location.session.title || "Untitled session";
		state = location.tier as "current" | "recent" | "archived";
		matches = (state === "archived") === operation.params.archived;
	} else {
		let project: NavigationProjectSummary | undefined;
		for (const catalog of ["projects", "archived_projects", "test_runs"]) {
			let offset = 0,
				remaining = 0,
				version: number | undefined;
			do {
				const response = await navigation.read({
					resource: "catalog",
					catalog,
					offset,
					limit: 50,
				});
				const page = response.data as NavigationProjectCatalog | null;
				if (!page || (version !== undefined && version !== response.version.revision))
					throw Error("Projects changed while checking this item. Refresh to try again.");
				version = response.version.revision;
				remaining = page.remaining;
				project = page.projects.find((row) => row.key === params.id);
				if (project || page.remaining === 0) break;
				if (page.projects.length === 0) throw Error("The project list did not advance.");
				offset += page.projects.length;
			} while (remaining > 0);
			if (project) break;
		}
		if (!project) throw Error("The previous project could not be found. Refresh to try again.");
		if (operation.kind === "archive" && project.working_dir !== operation.params.workingDir)
			throw Error("The project's working directory changed. Its organization could not be confirmed.");
		title = projectName(project);
		if (operation.kind === "favorite") {
			state = project.favorite ? "pinned" : "unpinned";
			matches = !!project.favorite === operation.params.favorited;
		} else {
			state = project.is_archived ? "archived" : "projects";
			matches = !!project.is_archived === operation.params.archived;
		}
	}
	await navigation.finish();
	// Effective archive placement can also come from age or project rules. It
	// cannot prove that an explicit archive decision with a lost reply persisted.
	return {
		generationId: navigation.generationId,
		title,
		state,
		settled: matches && (operation.kind === "favorite" || checkpoint.receipt !== null),
	};
}

// A project row this hub owns alone is the only one the source-less archive and
// favorite requests the phone sends can address: a project decision keys on (source, project
// ID), and an unqualified request is this hub's own decision, so a project whose
// rows also live on a host (or only on a host) would keep that host's old
// decision, which is exactly the merged-project gap. The web rail fans one
// request per owning source out and keeps a durable recovery record for it; this
// client has neither, so it withdraws the actions instead of half-applying them.
// The wire spells this hub's own source "local" and omits the field entirely for
// a controller-only project, which is what the summary reports here.
export function controllerOwnedProject(sources?: readonly string[]): boolean {
	return (sources ?? []).every((source) => source === "local");
}

/** A project's name as the phone shows it: its name, else its working
 * directory. */
export function projectName(project: NavigationProjectSummary): string {
	return project.name || project.working_dir || "Untitled project";
}

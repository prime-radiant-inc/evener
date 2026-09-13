import type {
	NavigationProjectCatalog,
	NavigationProjectSummary,
	NavigationSessionLocation,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { navigationReadback } from "./navigationReadback";
import { localSessionId } from "./sessionDeletionResult";

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
		throw Error(
			"Return to the previous organization screen to check that change.",
		);
	const { params } = operation;
	if (
		operation.kind === "archive" &&
		params.kind === "session" &&
		!localSessionId(`local:${params.id}`)
	)
		throw Error("Only local session archive changes can be checked here.");
	const navigation = await navigationReadback(
		client,
		checkpoint.receipt,
		current,
		confirmReceipt,
	);
	let title: string, state: OrganizationObservation["state"], matches: boolean;
	if (operation.kind === "archive" && params.kind === "session") {
		const ref = `local:${params.id}`;
		const response = await navigation.read({ resource: "location", ref });
		const location = response.data as NavigationSessionLocation | null;
		if (
			!location ||
			location.ref !== ref ||
			location.session?.ref !== ref ||
			location.session.session_id !== params.id ||
			location.session.host_id !== "local" ||
			!location.top_level ||
			!["current", "recent", "archived"].includes(location.tier ?? "")
		)
			throw Error(
				"This session's current organization could not be confirmed.",
			);
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
				if (
					!page ||
					(version !== undefined && version !== response.version.revision)
				)
					throw Error(
						"Projects changed while checking this item. Refresh to try again.",
					);
				version = response.version.revision;
				remaining = page.remaining;
				project = page.projects.find((row) => row.key === params.id);
				if (project || page.remaining === 0) break;
				if (page.projects.length === 0)
					throw Error("The project list did not advance.");
				offset += page.projects.length;
			} while (remaining > 0);
			if (project) break;
		}
		if (!project)
			throw Error(
				"The previous project could not be found. Refresh to try again.",
			);
		if (
			operation.kind === "archive" &&
			project.working_dir !== operation.params.workingDir
		)
			throw Error(
				"The project's working directory changed. Its organization could not be confirmed.",
			);
		title = project.name || project.working_dir || "Untitled project";
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
		settled:
			matches && (operation.kind === "favorite" || checkpoint.receipt !== null),
	};
}

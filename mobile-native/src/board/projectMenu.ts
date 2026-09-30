// A project row's long-press actions (ruling 15): the project changes the
// Projects screen offers, for projects the phone can organize today, and
// whether the organization journal holds one.
import type { NavigationProjectSummary } from "@evener/appwire-client";
import { controllerOwnedProject } from "../organizationNavigation";
import { journalOperation } from "./organizationCheck";
import type { BoardOrganization } from "./useBoardOrganization";

export type ProjectMenuAction = "pin" | "unpin" | "archive" | "unarchive";

export const PROJECT_MENU_LABELS: Record<ProjectMenuAction, string> = {
	pin: "Pin to top",
	unpin: "Unpin",
	archive: "Archive project",
	unarchive: "Unarchive project",
};

export function projectMenuActions(
	project: NavigationProjectSummary,
	context: { archived: boolean },
): ProjectMenuAction[] {
	// Offline, or with the journal busy, a change is held until it can go
	// (phase 6 ruling 18).
	if (project.key === "no-project" || !controllerOwnedProject(project.sources)) return [];
	const actions: ProjectMenuAction[] = [project.favorite ? "unpin" : "pin"];
	if (project.working_dir) actions.push(context.archived || project.is_archived ? "unarchive" : "archive");
	return actions;
}

/** The organization journal holds a change to this project, on its way or unresolved. */
export function journalHoldsProject(organization: BoardOrganization, projectKey: string): boolean {
	const operation = journalOperation(organization.state);
	if (operation?.kind === "favorite") return operation.params.id === projectKey;
	return operation?.kind === "archive" && operation.params.kind === "project" && operation.params.id === projectKey;
}

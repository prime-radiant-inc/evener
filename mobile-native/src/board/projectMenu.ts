// A project row's long-press actions (ruling 15): the project changes the
// Projects screen offers, for projects the phone can organize today.
import type { NavigationProjectSummary } from "@evener/appwire-client";
import { controllerOwnedProject } from "../organizationNavigation";

export type ProjectMenuAction = "pin" | "unpin" | "archive" | "unarchive";

export const PROJECT_MENU_LABELS: Record<ProjectMenuAction, string> = {
	pin: "Pin to top",
	unpin: "Unpin",
	archive: "Archive project",
	unarchive: "Unarchive project",
};

export function projectMenuActions(
	project: NavigationProjectSummary,
	context: { connected: boolean; organizationReady: boolean; archived: boolean },
): ProjectMenuAction[] {
	if (
		!context.connected ||
		!context.organizationReady ||
		project.key === "no-project" ||
		!controllerOwnedProject(project.sources)
	)
		return [];
	const actions: ProjectMenuAction[] = [project.favorite ? "unpin" : "pin"];
	if (project.working_dir)
		actions.push(context.archived || project.is_archived ? "unarchive" : "archive");
	return actions;
}

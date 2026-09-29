import type { NavigationProjectSummary } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { controllerOwnedProject } from "../organizationNavigation";
import { PROJECT_MENU_LABELS, projectMenuActions } from "./projectMenu";

const project = (over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 0,
	...over,
});
const ready = { connected: true, organizationReady: true, archived: false };

describe("a project row's long-press actions (ruling 15)", () => {
	it.each([
		["a local, unpinned project with a working directory", project(), ready, ["pin", "archive"]],
		["a pinned project", project({ favorite: true }), ready, ["unpin", "archive"]],
		["a project in the Archived section", project(), { ...ready, archived: true }, ["pin", "unarchive"]],
		["a project the hub marks archived", project({ is_archived: true }), ready, ["pin", "unarchive"]],
		["a project with no working directory", project({ working_dir: undefined }), ready, ["pin"]],
		["a project this hub owns, named", project({ sources: ["local"] }), ready, ["pin", "archive"]],
		["a project another host shares", project({ sources: ["local", "paradise-park"] }), ready, []],
		["a project only another host owns", project({ sources: ["paradise-park"] }), ready, []],
		["the sessions outside any project", project({ key: "no-project" }), ready, []],
		// Offline, a change is held until the connection returns (ruling 18).
		[
			"a project while offline",
			project(),
			{ ...ready, connected: false, organizationReady: false },
			["pin", "archive"],
		],
		[
			"a shared project while offline",
			project({ sources: ["local", "paradise-park"] }),
			{ ...ready, connected: false },
			[],
		],
		["any project while organization isn't ready", project(), { ...ready, organizationReady: false }, []],
	] as const)("%s", (_name, summary, context, expected) => {
		expect(projectMenuActions(summary, context)).toEqual(expected);
	});

	it("labels each action as the spec does", () => {
		expect(PROJECT_MENU_LABELS).toEqual({
			pin: "Pin to top",
			unpin: "Unpin",
			archive: "Archive project",
			unarchive: "Unarchive project",
		});
	});
});

describe("controllerOwnedProject", () => {
	it("is true for a project only this hub owns", () => {
		expect(controllerOwnedProject(undefined)).toBe(true);
		expect(controllerOwnedProject([])).toBe(true);
		expect(controllerOwnedProject(["local"])).toBe(true);
	});

	it("is false for a project another host shares", () => {
		expect(controllerOwnedProject(["local", "paradise-park"])).toBe(false);
	});
});

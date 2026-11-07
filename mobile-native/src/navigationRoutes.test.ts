import type { NavigationState } from "@react-navigation/native";
import { expect, it } from "vitest";
import { stackRoutes } from "./navigationRoutes";

const route = (name: string, params?: object, state?: unknown) => ({ key: name, name, params, state });

it("reports the split's detail routes after the Sessions route", () => {
	const state = {
		key: "root",
		index: 0,
		routes: [
			route("Sessions", undefined, {
				key: "detail",
				index: 2,
				routes: [
					route("DuoDetail"),
					route("Conversation", { hubId: "hub", ref: "session", title: "Session" }),
					route("TasksSheet", { hubId: "hub", ref: "session", threadId: "thread", hasTasks: true }),
				],
			}),
		],
	} as unknown as NavigationState;
	expect(stackRoutes(state).map((entry) => entry.name)).toEqual([
		"Sessions",
		"DuoDetail",
		"Conversation",
		"TasksSheet",
	]);
});

it("leaves a non-Sessions route's nested state out of the alert routes", () => {
	const state = {
		key: "root",
		index: 0,
		routes: [route("Projects", { hubId: "hub" }, { key: "nested", index: 0, routes: [route("Project")] })],
	} as unknown as NavigationState;
	expect(stackRoutes(state).map((entry) => entry.name)).toEqual(["Projects"]);
});

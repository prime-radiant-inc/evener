import type { NavigationState } from "@react-navigation/native";
import { expect, it } from "vitest";
import { alertScreenFor } from "./alerts/alertScreen";
import { activeConversation, stackRoutes } from "./navigationRoutes";

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

it("reads the empty split as the Board, not as another screen", () => {
	const state = {
		key: "root",
		index: 0,
		routes: [
			route("Sessions", undefined, {
				key: "detail",
				index: 0,
				routes: [route("Sessions")],
			}),
		],
	} as unknown as NavigationState;
	expect(activeConversation(state)).toBeNull();
	expect(alertScreenFor(stackRoutes(state), "hub")).toEqual({ kind: "board" });
});

it("reads the conversation under an open sheet", () => {
	const conversation = { hubId: "hub", ref: "session", title: "Session" };
	const state = {
		key: "root",
		index: 0,
		routes: [
			route("Sessions", undefined, {
				key: "detail",
				index: 2,
				routes: [
					route("Sessions"),
					route("Conversation", conversation),
					route("TasksSheet", { hubId: "hub", ref: "session", threadId: "thread", hasTasks: true }),
				],
			}),
		],
	} as unknown as NavigationState;
	expect(activeConversation(state)).toEqual(conversation);
});

it("has no active conversation outside the split", () => {
	const state = {
		key: "root",
		index: 0,
		routes: [route("Projects", { hubId: "hub" })],
	} as unknown as NavigationState;
	expect(activeConversation(state)).toBeNull();
});

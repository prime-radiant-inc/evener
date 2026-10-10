import { expect, it } from "vitest";
import { alertScreenFor, coversBanners } from "./alertScreen";

const board = { name: "Sessions" };
const session = (ref: string) => ({ name: "Conversation", params: { hubId: "hub-1", ref, title: ref } });
const tasks = { name: "TasksSheet", params: { hubId: "hub-1", ref: "local:a", threadId: "t", hasTasks: true } };
const hub = { name: "Hub", params: { screen: "HubHome" } };
const reader = { name: "Reader", params: { hubId: "hub-1", sessionRef: "local:a" } };

it.each([
	["the Board", [board], { kind: "board" }],
	["a session", [board, session("local:a")], { kind: "session", ref: "local:a" }],
	[
		"a sheet over a session, which keeps it in front",
		[board, session("local:a"), tasks],
		{ kind: "session", ref: "local:a" },
	],
	["the Hub over the Board", [board, hub], { kind: "board" }],
	["the Reader, which is its own screen", [board, session("local:a"), reader], { kind: "other" }],
	["a session route with no ref", [board, { name: "Conversation", params: {} }], { kind: "other" }],
	["nothing yet", [], { kind: "other" }],
] as const)("%s", (_name, routes, screen) => {
	expect(alertScreenFor(routes, "hub-1")).toEqual(screen);
});

it("counts a session screen only for its own hub, since refs are per hub", () => {
	expect(alertScreenFor([board, session("local:a")], "hub-2")).toEqual({ kind: "other" });
});

it("counts a sheet route and phase 5's modals as covering banners, and nothing else", () => {
	expect(coversBanners(tasks)).toBe(true);
	expect(coversBanners(hub)).toBe(true);
	expect(coversBanners({ name: "NewSession" })).toBe(true);
	expect(coversBanners(session("local:a"))).toBe(false);
	expect(coversBanners(reader)).toBe(false);
	expect(coversBanners(undefined)).toBe(false);
});

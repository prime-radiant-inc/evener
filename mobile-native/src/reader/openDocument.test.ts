// Opening a document from outside its session (spec 7.1).
import { StackActions, StackRouter } from "@react-navigation/routers";
import { expect, it } from "vitest";
import { openDocumentInSession } from "./openDocument";

const trail = {
	sessionRef: "local:fix",
	path: "docs/superpowers/plans/settle-race.md",
	reference: {
		path: "docs/superpowers/plans/settle-race.md",
		cwd: "/work/owner",
		readTarget: "/work/owner/docs/superpowers/plans/settle-race.md",
		provenance: "relative" as const,
	},
	sessionTitle: "Fix race",
	updatedAt: "2026-09-26T11:39:00.000Z",
};

it("opens the document inside its session, so Back lands in the session", () => {
	const pushed: [string, object][] = [];
	openDocumentInSession({ push: (name: string, params: object) => pushed.push([name, params]) } as never, {
		hubId: "studio",
		sessionRef: trail.sessionRef,
		path: trail.path,
		reference: trail.reference,
		sessionTitle: trail.sessionTitle,
		updatedAt: trail.updatedAt,
	});
	expect(pushed).toEqual([
		["Conversation", { hubId: "studio", ref: "local:fix", title: "Fix race" }],
		[
			"Reader",
			{
				hubId: "studio",
				sessionRef: trail.sessionRef,
				path: trail.path,
				reference: trail.reference,
				sessionTitle: trail.sessionTitle,
				updatedAt: trail.updatedAt,
			},
		],
	]);
});

it("opens a resumed message session under the original document route", () => {
	const router = StackRouter({ initialRouteName: "Sessions" });
	const options = { routeNames: ["Sessions", "Conversation", "Reader"], routeParamList: {}, routeGetIdList: {} };
	let state = router.getInitialState(options);
	openDocumentInSession(
		{
			push: (name: string, params: object) => {
				const next = router.getStateForAction(state, StackActions.push(name, params), options);
				if (!next) throw new Error("stack rejected document route");
				state = router.getRehydratedState(next, options);
			},
		} as never,
		{
			hubId: "studio",
			sessionRef: trail.sessionRef,
			messageSessionRef: "local:fix-2",
			path: trail.path,
			reference: trail.reference,
			sessionTitle: trail.sessionTitle,
			updatedAt: trail.updatedAt,
		},
	);
	expect(state.routes[1]).toMatchObject({ name: "Conversation", params: { ref: "local:fix-2" } });
	expect(state.routes[2]).toMatchObject({
		name: "Reader",
		params: { sessionRef: "local:fix", messageSessionRef: "local:fix-2", reference: trail.reference },
	});
});

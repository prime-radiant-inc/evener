import {
	type ArchiveParams,
	type FavoriteSetParams,
	isPlainObject,
	type NavigationMutation,
	type PinSectionDeleteParams,
	type PinSectionRenameParams,
	type SessionDeleteParams,
	type SessionPinAssignParams,
	type SessionPinUnpinParams,
	sameJsonValue,
} from "@evener/appwire-client";
import { cloneJson, hasExactKeys, isNonBlankText } from "./checkpointRecord";
import { decodeSessionDeletionResult, localSessionId, type SessionDeletionResult } from "./sessionDeletionResult";

export type NavigationOperation =
	| { kind: "archive"; params: ArchiveParams }
	| { kind: "favorite"; params: FavoriteSetParams }
	| { kind: "assignPin"; params: SessionPinAssignParams }
	| { kind: "unpin"; params: SessionPinUnpinParams }
	| { kind: "renamePinSection"; params: PinSectionRenameParams }
	| { kind: "deletePinSection"; params: PinSectionDeleteParams }
	| { kind: "deleteSession"; params: SessionDeleteParams };
export interface NavigationActionCheckpoint {
	id: string;
	operation: NavigationOperation;
	receipt: NavigationMutation | null;
	deletion?: SessionDeletionResult;
}
export interface NavigationActionStorage {
	load(): NavigationActionCheckpoint | null;
	begin(operation: NavigationOperation): NavigationActionCheckpoint;
	acknowledge(
		checkpoint: NavigationActionCheckpoint,
		receipt: NavigationMutation,
		deletion?: SessionDeletionResult,
	): NavigationActionCheckpoint;
	finish(checkpoint: NavigationActionCheckpoint): boolean;
}
export interface NavigationActionBackend {
	createId(): string;
	get(key: string): unknown;
	set(key: string, value: unknown): void;
	deleteIf(key: string, expected: unknown): boolean;
}
function valid(condition: unknown): asserts condition {
	if (!condition) throw Error("Invalid navigation recovery record.");
}
function record(value: unknown): Record<string, unknown> {
	valid(isPlainObject(value));
	return value;
}
function keys(value: Record<string, unknown>, required: string[], optional: string[] = []) {
	valid(hasExactKeys(value, required, optional));
}
const text = isNonBlankText;
/** A category's name is at most this many code points, the journal's own bound. */
export const CATEGORY_NAME_MAX_CODE_POINTS = 80;
function name(value: unknown) {
	return text(value) && Array.from(value.trim()).length <= CATEGORY_NAME_MAX_CODE_POINTS;
}
function validateOperation(value: unknown): NavigationOperation {
	const operation = record(value);
	keys(operation, ["kind", "params"]);
	const p = record(operation.params);
	switch (operation.kind) {
		case "archive":
			keys(p, ["kind", "id", "archived"], ["workingDir"]);
			valid(
				(p.kind === "session" || p.kind === "project") &&
					text(p.id) &&
					typeof p.archived === "boolean" &&
					(p.workingDir === undefined || text(p.workingDir)),
			);
			break;
		case "favorite":
			keys(p, ["kind", "id", "favorited"]);
			valid(p.kind === "project" && text(p.id) && typeof p.favorited === "boolean");
			break;
		case "assignPin":
			keys(p, ["sessionRef"], ["sectionId", "sectionName"]);
			valid(
				text(p.sessionRef) &&
					Object.hasOwn(p, "sectionId") !== Object.hasOwn(p, "sectionName") &&
					(Object.hasOwn(p, "sectionId") ? text(p.sectionId) : name(p.sectionName)),
			);
			break;
		case "unpin":
			keys(p, ["sessionRef"]);
			valid(text(p.sessionRef));
			break;
		case "renamePinSection":
			keys(p, ["sectionId", "name"]);
			valid(text(p.sectionId) && name(p.name));
			break;
		case "deletePinSection":
			keys(p, ["sectionId"]);
			valid(text(p.sectionId));
			break;
		case "deleteSession":
			keys(p, ["ref"]);
			valid(typeof p.ref === "string" && localSessionId(p.ref));
			break;
		default:
			throw Error("Invalid navigation recovery operation.");
	}
	return value as NavigationOperation;
}
function validateReceipt(value: unknown): NavigationMutation | null {
	if (value === null) return null;
	const receipt = record(value);
	keys(receipt, ["generation_id", "targets"]);
	valid(text(receipt.generation_id) && Array.isArray(receipt.targets));
	for (const raw of receipt.targets) {
		const target = record(raw);
		keys(target, ["kind"], ["section", "sectionId", "catalog", "projectKey", "revision"]);
		valid(
			["manifest", "section", "pin_catalog", "pin_section", "catalog", "project", "all_loaded_projects"].includes(
				String(target.kind),
			),
		);
		for (const key of ["section", "sectionId", "catalog", "projectKey"])
			if (Object.hasOwn(target, key)) valid(text(target[key]));
		if (target.kind === "section") valid(target.section === "live" || target.section === "needs_you");
		if (target.kind === "pin_section") valid(text(target.sectionId));
		if (target.kind === "catalog")
			valid(["projects", "archived_projects", "test_runs"].includes(String(target.catalog)));
		if (target.kind === "project") valid(text(target.projectKey));
		if (Object.hasOwn(target, "revision"))
			valid(Number.isSafeInteger(target.revision) && (target.revision as number) >= 0);
	}
	return value as NavigationMutation;
}
function validateCheckpoint(value: unknown): NavigationActionCheckpoint {
	const checkpoint = record(value);
	keys(checkpoint, ["id", "operation", "receipt"], ["deletion"]);
	valid(text(checkpoint.id));
	const operation = validateOperation(checkpoint.operation);
	validateReceipt(checkpoint.receipt);
	if (Object.hasOwn(checkpoint, "deletion")) {
		valid(operation.kind === "deleteSession" && checkpoint.receipt !== null);
		decodeSessionDeletionResult(checkpoint.deletion);
	}
	return value as NavigationActionCheckpoint;
}

export function nativeNavigationActions(hubId: string, backend: NavigationActionBackend): NavigationActionStorage {
	valid(text(hubId));
	const key = `evener.native.navigation-action.${hubId}`;
	const load = (): NavigationActionCheckpoint | null => {
		const raw = backend.get(key);
		return raw === null || raw === undefined ? null : cloneJson(validateCheckpoint(raw));
	};
	return {
		load,
		begin(operation) {
			validateOperation(operation);
			valid(load() === null);
			const id = backend.createId();
			valid(text(id));
			const checkpoint = { id, operation: cloneJson(operation), receipt: null };
			// Calls stay synchronous so admission cannot yield between checking and saving.
			backend.set(key, cloneJson(checkpoint));
			return cloneJson(checkpoint);
		},
		acknowledge(checkpoint, receipt, deletion) {
			validateCheckpoint(checkpoint);
			valid(validateReceipt(receipt) !== null);
			const current = load();
			valid(current !== null && sameJsonValue(current, checkpoint));
			const next = {
				...current,
				receipt: cloneJson(receipt),
				...(deletion ? { deletion: cloneJson(deletion) } : {}),
			};
			validateCheckpoint(next);
			backend.set(key, cloneJson(next));
			return cloneJson(next);
		},
		finish(checkpoint) {
			validateCheckpoint(checkpoint);
			const current = load();
			if (current === null || !sameJsonValue(current, checkpoint)) return false;
			return backend.deleteIf(key, cloneJson(current));
		},
	};
}

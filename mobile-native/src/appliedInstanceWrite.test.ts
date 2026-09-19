import { expect, it } from "vitest";
import {
	ErrorInstanceRemoveApplied,
	ErrorInstanceRenamePersisted,
	WireError,
} from "@evener/appwire-client";
import { appliedInstanceWrite } from "./appliedInstanceWrite";

it("classifies the hub's applied provider-instance writes", () => {
	expect(
		appliedInstanceWrite(
			new WireError("the stored key was left behind", -32603, {
				evenerErrorInfo: ErrorInstanceRemoveApplied,
			}),
		),
	).toBe("remove");
	expect(
		appliedInstanceWrite(
			new WireError("the record was left behind", -32603, {
				evenerErrorInfo: ErrorInstanceRenamePersisted,
			}),
		),
	).toBe("rename");
});

it("leaves every ordinary rejection to the generic failure path", () => {
	// Same code as the applied writes, different discriminator: the code is
	// never what classifies.
	expect(
		appliedInstanceWrite(
			new WireError("old no longer resolves", -32603, {
				evenerErrorInfo: "conflict",
			}),
		),
	).toBeNull();
	expect(appliedInstanceWrite(new WireError("plain", -32603))).toBeNull();
	expect(appliedInstanceWrite(new Error("plain"))).toBeNull();
	expect(appliedInstanceWrite("plain")).toBeNull();
	expect(appliedInstanceWrite(null)).toBeNull();
});

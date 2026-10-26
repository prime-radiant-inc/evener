// @vitest-environment node
import { expect, test } from "vitest";
import excerptGo from "../../appwire/excerpt.go?raw";
import docServeGo from "../../cmd/evener-hub/doc_serve.go?raw";
import hubcoreTypesGo from "../../cmd/evener-hub/internal/hubcore/types.go?raw";
import { MAX_ATTACHMENT_BYTES, MAX_ATTACHMENTS } from "./attachmentLimits";
import { DOC_FILE_MAX_BYTES } from "./docContent";
import { MAX_INTENT_CODE_POINTS } from "./sessionActivity";
import { goIntConstantValue } from "./testing/goConstants";

// Limits the client enforces before a request leaves, retyped from the Go
// constants that refuse the request on the hub. A change on one side without
// the other either rejects what the hub accepts or sends what it refuses.

test("the attachment limits equal the hub's send limits", () => {
  expect(MAX_ATTACHMENTS).toBe(goIntConstantValue(hubcoreTypesGo, "SendMaxImageItems", "hubcore/types.go"));
  expect(MAX_ATTACHMENT_BYTES).toBe(goIntConstantValue(hubcoreTypesGo, "SendMaxImageBytes", "hubcore/types.go"));
});

test("the document file limit equals the hub's", () => {
  expect(DOC_FILE_MAX_BYTES).toBe(goIntConstantValue(docServeGo, "docFileMaxBytes", "doc_serve.go"));
});

test("the intent length limit equals the wire's", () => {
  expect(MAX_INTENT_CODE_POINTS).toBe(goIntConstantValue(excerptGo, "MaxIntentRunes", "appwire/excerpt.go"));
});

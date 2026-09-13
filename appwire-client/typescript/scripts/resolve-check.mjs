// Runtime resolution proof for the native trees, run inside the qualification
// runner's packed consumer.
//
// mobile-native and mobile/src import this package by name, but neither
// declares a dependency on it: both resolve the name through a repo alias
// (tsconfig paths, the vitest config, Metro's resolveRequest) onto TypeScript
// source. So `make test-native` proves the aliases agree with the source tree
// and proves nothing about the package a consumer actually installs. This file
// runs where that question can be answered: against the tarball, unpacked into
// node_modules, loaded by plain Node.
//
// It names every runtime VALUE the native trees take from the package. Types
// are out of scope - they are erased before anything runs. qualify-package.mjs
// re-derives both lists from the native import graph and fails if they have
// drifted, so a value native starts importing cannot slip past this check.
import assert from "node:assert/strict";
import {
  ActivityList,
  AppwireClient,
  buildActivityRows,
  buildComposerInput,
  buildInput,
  composeAskAnswers,
  decideSteerRoute,
  filterSlashMenuItems,
  formatElapsed,
  friendlyErrorMessage,
  hasItemFailure,
  isActionUnavailable,
  isInProgressStatus,
  isStaleCursorError,
  isThreadNotFound,
  mergeSlashCommands,
  parseActivityTree,
  parseJobLogTail,
  parseSlashToken,
  reconcileBatches,
  sessionActionError,
  spliceSlashCommand,
  splitMandate,
  stableDelegateDisplayStatus,
  translateAttachmentMarkers,
  visibleCatalogCommands,
  WireError,
} from "@evener/appwire-client";
import { docImageURL, readDocFile } from "@evener/appwire-client/docContent";

// Parsed out of this file by qualify-package.mjs and checked against the
// native import graph, so keep both as plain arrays of string literals.
const ROOT_VALUES = [
  "ActivityList",
  "AppwireClient",
  "WireError",
  "buildActivityRows",
  "buildComposerInput",
  "buildInput",
  "composeAskAnswers",
  "decideSteerRoute",
  "filterSlashMenuItems",
  "formatElapsed",
  "friendlyErrorMessage",
  "hasItemFailure",
  "isActionUnavailable",
  "isInProgressStatus",
  "isStaleCursorError",
  "isThreadNotFound",
  "mergeSlashCommands",
  "parseActivityTree",
  "parseJobLogTail",
  "parseSlashToken",
  "reconcileBatches",
  "sessionActionError",
  "spliceSlashCommand",
  "splitMandate",
  "stableDelegateDisplayStatus",
  "translateAttachmentMarkers",
  "visibleCatalogCommands",
];
const DOC_CONTENT_VALUES = ["docImageURL", "readDocFile"];

const bound = {
  ActivityList,
  AppwireClient,
  WireError,
  buildActivityRows,
  buildComposerInput,
  buildInput,
  composeAskAnswers,
  decideSteerRoute,
  filterSlashMenuItems,
  formatElapsed,
  friendlyErrorMessage,
  hasItemFailure,
  isActionUnavailable,
  isInProgressStatus,
  isStaleCursorError,
  isThreadNotFound,
  mergeSlashCommands,
  parseActivityTree,
  parseJobLogTail,
  parseSlashToken,
  reconcileBatches,
  sessionActionError,
  spliceSlashCommand,
  splitMandate,
  stableDelegateDisplayStatus,
  translateAttachmentMarkers,
  visibleCatalogCommands,
  docImageURL,
  readDocFile,
};

// The import statements above are already most of the proof - Node throws a
// SyntaxError for a named import the installed CommonJS build does not export,
// before a line of this runs. What the loop adds is that each name arrived as
// something callable, and that these two lists stay load-bearing at run time
// rather than becoming a comment the runner alone reads.
for (const name of [...ROOT_VALUES, ...DOC_CONTENT_VALUES]) {
  assert.equal(typeof bound[name], "function", `native imports ${name}, which the installed package does not provide`);
}

// readDocFile needs a DocPort from the host and is deliberately not called.
assert.equal(docImageURL("", "s 1", "a b.png"), "/doc/image?session=s%201&path=a%20b.png");
assert.equal(formatElapsed(0), "0s");

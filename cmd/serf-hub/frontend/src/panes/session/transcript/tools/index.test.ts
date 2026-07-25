import { expect, test } from "vitest";
import "./index";
import { RawToolOutput } from "../RawToolOutput";
import { toolRendererFor } from "../toolRenderers";

// A single import of this barrel must be enough to register every T3
// descriptor - the smoke test every registration file's own test already
// covers individually, but this proves the BARREL ITSELF (the one import
// line the integration handoff actually uses) does the job, not just
// each file in isolation.
test("importing the barrel registers descriptors for every T3 tool family", () => {
  const registered = [
    "read_file",
    "grep",
    "list_dir",
    "glob",
    "shell",
    "edit_file",
    "write_file",
    "apply_patch",
    "web_fetch",
    "web_search",
    "use_skill",
    "job_status",
    "job_list",
    "job_stop",
    "delegate_send",
    "delegate",
    "ask_user",
    "task_list",
    "read_transcript",
    "read_session_transcript",
  ];
  for (const name of registered) {
    expect(toolRendererFor(name).body).not.toBe(RawToolOutput);
  }
});

test("an unregistered tool name still falls back to the raw default (the barrel doesn't break the fallback)", () => {
  expect(toolRendererFor("totally_unregistered_tool_xyz").body).toBe(RawToolOutput);
});

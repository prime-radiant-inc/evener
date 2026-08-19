// GREEN test for issue #199: LaunchConfigForm.test.tsx flakes — toast store leak.
//
// Root cause: `LaunchConfigForm`'s `handleSubmit` catch unconditionally pushes
// 'Save failed' into the module-singleton toast store (widgets/toast/store.ts).
// The dismiss timer only starts inside `<Toast/>`'s `ToastItem` mount effect,
// NOT in `pushToast` itself. The "env-credential" test in LaunchConfigForm.test.tsx
// renders the form WITHOUT `<Toast/>`, so its pushed 'Save failed' toast sits in
// the singleton forever (no `<Toast/>` mounted -> no ToastItem -> no timer).
// `LaunchConfigForm.test.tsx`'s afterEach called `cleanup()` and
// `vi.useRealTimers()` but NEVER `resetToastStoreForTests()`, so the toast
// survived into the next test. When `--sequence.shuffle` ran the env-credential
// test before the status test, the status test's `getByText('Save failed')`
// found 2 elements (the leaked toast + its own) and threw.
//
// This file reproduces that leak DETERMINISTICALLY in the default (non-shuffled)
// order by deliberately rendering the leaking test first and the asserting test
// second. The fix is the per-test `resetToastStoreForTests()` in `afterEach`
// below — mirroring the convention `widgets/toast/toast.test.tsx` and
// `shell/rail/Rail.test.tsx` already follow — so test 1's stranded toast is
// cleared before test 2 renders. This test now guards that cleanup: drop the
// reset and test 2 goes RED again (it sees the leaked + its own toast = 2).
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test } from "vitest";
import type { LaunchOption, PathValidateResponse } from "../../../../protocol/types.gen";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { LaunchConfigForm } from "./LaunchConfigForm";

afterEach(() => {
  cleanup();
  // handleSubmit's catch pushes "Save failed" into the module-singleton toast
  // store regardless of whether <Toast/> is mounted; an earlier failed-save
  // test's undisplayed toast otherwise outlives cleanup() and collides with a
  // later test's singular 'Save failed' assertion. isolate:true only resets the
  // toast module at file boundaries, not between tests in the same file.
  resetToastStoreForTests();
});

const OPTIONS: LaunchOption[] = [
  {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "Agent",
    kind: "text",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "env",
    wireField: "env",
    label: "Environment variables",
    group: "Environment",
    kind: "envMap",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
];

const OK_VALIDATE: (path: string, kind: string) => Promise<PathValidateResponse> = async (path) => ({
  path,
  valid: true,
});

describe("issue #199 — toast store leak across tests", () => {
  // Test 1 (the leaker): mirrors the "env-credential" test pattern — render the
  // form WITHOUT <Toast/>, trigger a failing save. handleSubmit's catch pushes
  // 'Save failed' into the singleton store, but with no <Toast/> mounted there
  // is no ToastItem to schedule a dismiss timer, so the toast would persist in
  // the module singleton after cleanup() unmounts the form — except that this
  // file's afterEach now resets the store, so it cannot leak into test 2.
  test("1. a failing save without <Toast/> leaves a 'Save failed' toast stranded in the singleton", async () => {
    const user = userEvent.setup();
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => {
          throw new Error('env key "FOO" looks like a credential; route through evener/auth/apiKey/set');
        }}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    // Confirm the save actually failed (env-credential error surfaces inline),
    // which is what drove the 'Save failed' toast push in handleSubmit's catch.
    await waitFor(() => expect(screen.getByText(/looks like a credential/)).toBeTruthy());
  });

  // Test 2 (the victim): mirrors the "status shows Error" test pattern — render
  // the form WITH <Toast/>, trigger a failing save, then assert 'Save failed'
  // appears exactly once. Without the per-test reset, <Toast/> would render
  // BOTH the stranded toast from test 1 AND this test's own new toast, so
  // getAllByText would return 2 instead of 1.
  test("2. a second failing save with <Toast/> sees exactly one 'Save failed'", async () => {
    const user = userEvent.setup();
    render(
      <>
        <LaunchConfigForm
          options={OPTIONS}
          layer="global"
          current={{}}
          successToast="Launch defaults saved"
          validatePath={OK_VALIDATE}
          onSave={async () => {
            throw new Error("disk full");
          }}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText("Error: disk full")).toBeTruthy());
    expect(screen.getAllByText("Save failed")).toHaveLength(1);
  });
});

import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { PendingChips } from "./PendingChips";

afterEach(() => cleanup());

// T1 mounts this stub beside the composer; wave-8 T4 fills the chips. Until
// then it renders nothing (no DOM node), so mounting it in Session.tsx is a
// no-op that can't disturb the composer/transcript layout around it.
test("PendingChips renders nothing until T4 fills it", () => {
  const { container } = render(<PendingChips sessionRef="ref_a" />);
  expect(container.innerHTML).toBe("");
});

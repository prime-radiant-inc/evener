import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { ForceStopDialog } from "./ForceStopDialog";

test("retains pending confirmation through Cancel and Escape, then permits retry after failure", async () => {
  let rejectStop: (error: Error) => void = () => {};
  const pending = new Promise<void>((_, reject) => {
    rejectStop = reject;
  });
  const onConfirm = vi.fn(() => pending);
  const onClose = vi.fn();
  render(<ForceStopDialog open onClose={onClose} onConfirm={onConfirm} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Force shutdown" }));
  expect(onConfirm).toHaveBeenCalledTimes(1);
  const cancel = screen.getByRole("button", { name: "Cancel" });
  expect((cancel as HTMLButtonElement).disabled).toBe(true);
  await user.click(cancel);
  await user.keyboard("{Escape}");
  expect(onClose).not.toHaveBeenCalled();
  await act(async () => rejectStop(new Error("exit unconfirmed")));
  await waitFor(() => expect((cancel as HTMLButtonElement).disabled).toBe(false));
  expect((screen.getByRole("button", { name: "Force shutdown" }) as HTMLButtonElement).disabled).toBe(false);
  await user.click(cancel);
  expect(onClose).toHaveBeenCalledTimes(1);
});

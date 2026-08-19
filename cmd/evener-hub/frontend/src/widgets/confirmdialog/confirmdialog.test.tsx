import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ConfirmDialog } from "./index";

afterEach(cleanup);

test("renders nothing when closed", () => {
  render(
    <ConfirmDialog open={false} title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={vi.fn()}>
      Body
    </ConfirmDialog>,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("renders as a dialog with the given title and body", () => {
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={vi.fn()}>
      This will also clear its stored credentials.
    </ConfirmDialog>,
  );
  expect(screen.getByRole("dialog", { name: "Remove instance" })).toBeTruthy();
  expect(screen.getByText("This will also clear its stored credentials.")).toBeTruthy();
});

test("renders a Cancel button and the given confirm-verb button", () => {
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={vi.fn()}>
      Body
    </ConfirmDialog>,
  );
  expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
});

test("a custom cancelLabel replaces the default Cancel text", () => {
  render(
    <ConfirmDialog
      open
      title="Discard draft"
      confirmLabel="Discard"
      cancelLabel="Keep editing"
      onConfirm={vi.fn()}
      onCancel={vi.fn()}
    >
      Body
    </ConfirmDialog>,
  );
  expect(screen.getByRole("button", { name: "Keep editing" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});

test("clicking Cancel calls onCancel, not onConfirm", async () => {
  const user = userEvent.setup();
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={onConfirm} onCancel={onCancel}>
      Body
    </ConfirmDialog>,
  );
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onCancel).toHaveBeenCalledOnce();
  expect(onConfirm).not.toHaveBeenCalled();
});

test("clicking the confirm button calls onConfirm, not onCancel", async () => {
  const user = userEvent.setup();
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={onConfirm} onCancel={onCancel}>
      Body
    </ConfirmDialog>,
  );
  await user.click(screen.getByRole("button", { name: "Remove" }));
  expect(onConfirm).toHaveBeenCalledOnce();
  expect(onCancel).not.toHaveBeenCalled();
});

test("Escape calls onCancel (Dialog's own onClose contract)", async () => {
  const user = userEvent.setup();
  const onCancel = vi.fn();
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={onCancel}>
      Body
    </ConfirmDialog>,
  );
  await user.keyboard("{Escape}");
  expect(onCancel).toHaveBeenCalledOnce();
});

test("destructive defaults to true: the confirm button's class differs from a non-destructive one", () => {
  const { unmount } = render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={vi.fn()}>
      Body
    </ConfirmDialog>,
  );
  const destructiveClass = screen.getByRole("button", { name: "Remove" }).className;
  unmount();

  render(
    <ConfirmDialog
      open
      title="Install plugin"
      confirmLabel="Install"
      destructive={false}
      onConfirm={vi.fn()}
      onCancel={vi.fn()}
    >
      Body
    </ConfirmDialog>,
  );
  const nonDestructiveClass = screen.getByRole("button", { name: "Install" }).className;

  expect(destructiveClass).not.toBe(nonDestructiveClass);
});

test("busy disables both buttons", () => {
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" busy onConfirm={vi.fn()} onCancel={vi.fn()}>
      Body
    </ConfirmDialog>,
  );
  expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
});

test("busy defaults to false: buttons are enabled", () => {
  render(
    <ConfirmDialog open title="Remove instance" confirmLabel="Remove" onConfirm={vi.fn()} onCancel={vi.fn()}>
      Body
    </ConfirmDialog>,
  );
  expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(false);
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

import { fireEvent, render, screen } from "@testing-library/react";
import type { KeyboardEvent } from "react";
import { expect, test, vi } from "vitest";
import { useEscapeToClose } from "./useEscapeToClose";

function Layer({
  onClose,
  stopPropagation,
  onAncestor,
}: {
  onClose: () => void;
  stopPropagation?: boolean;
  onAncestor: (event: KeyboardEvent<HTMLDivElement>) => void;
}) {
  const handleEscape = useEscapeToClose(onClose, { stopPropagation });
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: test-only event-bubbling probe, not a rendered widget
    <div onKeyDown={onAncestor}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: test-only event-bubbling probe, not a rendered widget */}
      <div data-testid="layer" onKeyDown={handleEscape} />
    </div>
  );
}

test("Escape closes the layer and is consumed", () => {
  const onClose = vi.fn();
  const onAncestor = vi.fn();
  render(<Layer onClose={onClose} stopPropagation onAncestor={onAncestor} />);
  fireEvent.keyDown(screen.getByTestId("layer"), { key: "Escape" });
  expect(onClose).toHaveBeenCalledOnce();
  expect(onAncestor).not.toHaveBeenCalled();
});

test("without stopPropagation the press still reaches an ancestor", () => {
  const onClose = vi.fn();
  const onAncestor = vi.fn();
  render(<Layer onClose={onClose} onAncestor={onAncestor} />);
  fireEvent.keyDown(screen.getByTestId("layer"), { key: "Escape" });
  expect(onClose).toHaveBeenCalledOnce();
  expect(onAncestor).toHaveBeenCalledOnce();
});

test("any other key is left to its own handler", () => {
  const onClose = vi.fn();
  const onAncestor = vi.fn();
  render(<Layer onClose={onClose} stopPropagation onAncestor={onAncestor} />);
  fireEvent.keyDown(screen.getByTestId("layer"), { key: "Enter" });
  expect(onClose).not.toHaveBeenCalled();
  expect(onAncestor).toHaveBeenCalledOnce();
});

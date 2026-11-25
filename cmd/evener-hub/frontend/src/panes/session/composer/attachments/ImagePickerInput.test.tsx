import { fireEvent, render } from "@testing-library/react";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { ImagePickerInput } from "./ImagePickerInput";

test("reports the picked files through the shared change handler", () => {
  const inputRef = createRef<HTMLInputElement>();
  const onFiles = vi.fn();
  const { container } = render(<ImagePickerInput inputRef={inputRef} onFiles={onFiles} />);
  const file = new File(["x"], "picked.png", { type: "image/png" });

  fireEvent.change(container.querySelector('input[type="file"]') as HTMLInputElement, {
    target: { files: [file] },
  });

  expect(onFiles).toHaveBeenCalledWith([file]);
  // AttachImageControl reaches the picker by clicking this ref's element.
  expect(inputRef.current).toBe(container.querySelector('input[type="file"]'));
});

test("clears the input's value so re-picking the identical file fires change again", () => {
  const onFiles = vi.fn();
  const { container } = render(<ImagePickerInput inputRef={createRef<HTMLInputElement>()} onFiles={onFiles} />);
  const input = container.querySelector('input[type="file"]') as HTMLInputElement;

  fireEvent.change(input, { target: { files: [new File(["x"], "again.png", { type: "image/png" })] } });

  expect(input.value).toBe("");
});

test("an empty pick reports nothing", () => {
  const onFiles = vi.fn();
  const { container } = render(<ImagePickerInput inputRef={createRef<HTMLInputElement>()} onFiles={onFiles} />);

  fireEvent.change(container.querySelector('input[type="file"]') as HTMLInputElement, { target: { files: [] } });

  expect(onFiles).not.toHaveBeenCalled();
});

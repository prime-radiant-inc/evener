import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Dropzone } from "./index";

afterEach(cleanup);

function makeFile(name: string): File {
  return new File(["x"], name, { type: "image/png" });
}

test("renders its children", () => {
  render(
    <Dropzone onFiles={() => {}}>
      <p>drop here</p>
    </Dropzone>,
  );
  // getByText throws if not found - the successful lookup itself is the
  // assertion (no jest-dom matchers in this codebase, see other widgets).
  expect(screen.getByText("drop here").textContent).toBe("drop here");
});

test("calls onFiles with the dropped File[]", () => {
  const onFiles = vi.fn();
  render(
    <Dropzone onFiles={onFiles}>
      <p>zone</p>
    </Dropzone>,
  );
  const file = makeFile("a.png");
  fireEvent.drop(screen.getByText("zone").parentElement as HTMLElement, { dataTransfer: { files: [file] } });
  expect(onFiles).toHaveBeenCalledTimes(1);
  expect(onFiles.mock.calls[0]?.[0]).toEqual([file]);
});

test("passes through every dropped file, in order, for the caller to filter/validate", () => {
  const onFiles = vi.fn();
  render(
    <Dropzone onFiles={onFiles}>
      <p>zone</p>
    </Dropzone>,
  );
  const a = makeFile("a.png");
  const b = makeFile("b.png");
  fireEvent.drop(screen.getByText("zone").parentElement as HTMLElement, { dataTransfer: { files: [a, b] } });
  expect(onFiles).toHaveBeenCalledWith([a, b]);
});

test("a drop with no files calls onFiles with nothing to do", () => {
  const onFiles = vi.fn();
  render(
    <Dropzone onFiles={onFiles}>
      <p>zone</p>
    </Dropzone>,
  );
  fireEvent.drop(screen.getByText("zone").parentElement as HTMLElement, { dataTransfer: { files: [] } });
  expect(onFiles).not.toHaveBeenCalled();
});

test("preventDefault's the drop event so the browser doesn't navigate to the file", () => {
  render(
    <Dropzone onFiles={() => {}}>
      <p>zone</p>
    </Dropzone>,
  );
  const zone = screen.getByText("zone").parentElement as HTMLElement;
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: { files: [] } });
  zone.dispatchEvent(event);
  expect(event.defaultPrevented).toBe(true);
});

test("preventDefault's dragover so a drop event can fire at all", () => {
  render(
    <Dropzone onFiles={() => {}}>
      <p>zone</p>
    </Dropzone>,
  );
  const zone = screen.getByText("zone").parentElement as HTMLElement;
  const event = new Event("dragover", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: { files: [] } });
  zone.dispatchEvent(event);
  expect(event.defaultPrevented).toBe(true);
});

test("dragenter marks the zone active; dragleave clears it", () => {
  render(
    <Dropzone onFiles={() => {}}>
      <p>zone</p>
    </Dropzone>,
  );
  const zone = screen.getByText("zone").parentElement as HTMLElement;
  const activeClass = Array.from(zone.classList).find((c) => c !== zone.classList[0]);

  fireEvent.dragEnter(zone, { dataTransfer: { files: [] } });
  expect(zone.className).not.toBe("");
  const classesAfterEnter = new Set(zone.classList);

  fireEvent.dragLeave(zone, { dataTransfer: { files: [] } });
  const classesAfterLeave = new Set(zone.classList);
  expect(classesAfterLeave.size).toBeLessThan(classesAfterEnter.size);
  // Sanity: whatever class dragenter added is gone again after dragleave.
  const added = [...classesAfterEnter].find((c) => !classesAfterLeave.has(c));
  expect(added).toBeDefined();
  expect(activeClass === undefined || !classesAfterLeave.has(activeClass)).toBe(true);
});

test("a drop clears the active state (not left stuck highlighted)", () => {
  render(
    <Dropzone onFiles={() => {}}>
      <p>zone</p>
    </Dropzone>,
  );
  const zone = screen.getByText("zone").parentElement as HTMLElement;
  fireEvent.dragEnter(zone, { dataTransfer: { files: [] } });
  const classesAfterEnter = new Set(zone.classList);

  fireEvent.drop(zone, { dataTransfer: { files: [makeFile("a.png")] } });
  const classesAfterDrop = new Set(zone.classList);
  expect(classesAfterDrop.size).toBeLessThan(classesAfterEnter.size);
});

test("disabled: does not call onFiles on drop", () => {
  const onFiles = vi.fn();
  render(
    <Dropzone onFiles={onFiles} disabled>
      <p>zone</p>
    </Dropzone>,
  );
  fireEvent.drop(screen.getByText("zone").parentElement as HTMLElement, {
    dataTransfer: { files: [makeFile("a.png")] },
  });
  expect(onFiles).not.toHaveBeenCalled();
});

test("disabled: dragenter does not mark the zone active", () => {
  render(
    <Dropzone onFiles={() => {}} disabled>
      <p>zone</p>
    </Dropzone>,
  );
  const zone = screen.getByText("zone").parentElement as HTMLElement;
  const classesBefore = new Set(zone.classList);
  fireEvent.dragEnter(zone, { dataTransfer: { files: [] } });
  expect(new Set(zone.classList)).toEqual(classesBefore);
});

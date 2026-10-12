import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { Code, FieldDim } from "./settingsField";

describe("FieldDim", () => {
  test("renders its children in a span", () => {
    render(<FieldDim>created 3d ago</FieldDim>);
    expect(screen.getByText("created 3d ago").tagName).toBe("SPAN");
  });
});

describe("Code", () => {
  test("renders its children in a code element", () => {
    render(<Code>hub.toml</Code>);
    expect(screen.getByText("hub.toml").tagName).toBe("CODE");
  });
});

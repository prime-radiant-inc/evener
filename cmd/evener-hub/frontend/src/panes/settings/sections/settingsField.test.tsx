import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { PropertyList } from "../../../widgets";
import { Code, FieldDim, FieldValue } from "./settingsField";
import { settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";

describe("FieldValue", () => {
  test("tags a PropertyList row's value with its field id, so the row is findable without its label", () => {
    const { container } = render(
      <PropertyList
        layout="stacked"
        rows={[{ label: "Label fixture", value: <FieldValue field="run-dir">/tmp/run</FieldValue> }]}
      />,
    );
    expect(settingsFieldIds(container)).toEqual(["run-dir"]);
    expect(settingsFieldValue("run-dir", container).textContent).toBe("/tmp/run");
  });
});

describe("FieldDim", () => {
  test("renders its children in a span", () => {
    render(<FieldDim>dim fixture</FieldDim>);
    expect(screen.getByText("dim fixture").tagName).toBe("SPAN");
  });
});

describe("Code", () => {
  test("renders its children in a code element", () => {
    render(<Code>hub.toml</Code>);
    expect(screen.getByText("hub.toml").tagName).toBe("CODE");
  });
});

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test } from "vitest";
import { Code, FieldDim, SettingsField } from "./settingsField";

afterEach(cleanup);

describe("SettingsField", () => {
  test("renders the label as a definition term and the value as its definition", () => {
    render(
      <dl>
        <SettingsField label="Hub address" value="127.0.0.1:9180" />
      </dl>,
    );
    expect(screen.getByText("Hub address").tagName).toBe("DT");
    expect(screen.getByText("127.0.0.1:9180").tagName).toBe("DD");
  });

  test("omits the help paragraph when not provided", () => {
    const { container } = render(
      <dl>
        <SettingsField label="Run dir" value="/tmp/run" />
      </dl>,
    );
    expect(container.querySelector("p")).toBeNull();
  });

  test("renders the help paragraph when provided", () => {
    render(
      <dl>
        <SettingsField label="Run dir" value="/tmp/run" help="Per-PID rendezvous files." />
      </dl>,
    );
    expect(screen.getByText("Per-PID rendezvous files.").tagName).toBe("P");
  });

  test("value accepts rich content (e.g. a value plus a dim trailing note)", () => {
    render(
      <dl>
        <SettingsField
          label="Hub version"
          value={
            <>
              1.2.3 <FieldDim>(abc1234)</FieldDim>
            </>
          }
        />
      </dl>,
    );
    expect(screen.getByText("1.2.3", { exact: false })).toBeTruthy();
    expect(screen.getByText("(abc1234)")).toBeTruthy();
  });
});

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

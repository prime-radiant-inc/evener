import { useEffect, useRef, useState } from "react";
import { SegmentedControl, type SegmentedControlOption } from "../../widgets/segmentedcontrol";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./segmentedcontrol.module.css";

const OPTIONS: readonly SegmentedControlOption[] = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
  { value: "activity", label: "Activity" },
  { value: "full", label: "Full", accessibleLabel: "Full detail" },
  { value: "custom", label: "Custom" },
];

const SHORT_OPTIONS: readonly SegmentedControlOption[] = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
];

function InteractiveControl() {
  const [value, setValue] = useState("tools");
  return <SegmentedControl label="Interactive detail" value={value} options={SHORT_OPTIONS} onChange={setValue} />;
}

function FocusControl() {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    host.current?.querySelector<HTMLButtonElement>('[role="radio"][aria-checked="true"]')?.focus();
  }, []);
  return (
    <div ref={host}>
      <SegmentedControl label="Keyboard focus" value="intent" options={SHORT_OPTIONS} onChange={() => {}} />
    </div>
  );
}

function SelectedExample({ label, value }: { label: string; value: string }) {
  return <SegmentedControl label={label} value={value} options={OPTIONS} onChange={() => {}} />;
}

function NarrowFrame({ width }: { width: 320 | 390 }) {
  return (
    <div className={width === 320 ? styles.frame320 : styles.frame390} data-testid={`segmentedcontrol-frame-${width}`}>
      <SegmentedControl
        label={`${width}px narrow frame`}
        value="tools"
        options={OPTIONS}
        onChange={() => {}}
        fullWidth
      />
    </div>
  );
}

export default function SegmentedControlGallerySection() {
  return (
    <section data-testid="segmentedcontrol-gallery">
      <h2>SegmentedControl</h2>
      <ThemeFlip>
        <div className={styles.stack}>
          <div className={styles.row}>
            <InteractiveControl />
            <SegmentedControl
              label="Small detail"
              value="intent"
              options={SHORT_OPTIONS}
              onChange={() => {}}
              size="sm"
            />
          </div>
          <div className={styles.fullWidthDemo} data-testid="segmentedcontrol-full-width-demo">
            <SegmentedControl
              label="Six-option transcript detail"
              value="tools"
              options={OPTIONS}
              onChange={() => {}}
              fullWidth
            />
          </div>
          <div className={styles.row}>
            <SelectedExample label="First selected" value="chat" />
            <SelectedExample label="Middle selected" value="tools" />
            <SelectedExample label="Last selected" value="custom" />
            <SelectedExample label="Custom selected" value="custom" />
            <SelectedExample label="Full selected" value="full" />
          </div>
          <div className={styles.row}>
            <SegmentedControl
              label="Disabled option"
              value="chat"
              options={[
                { value: "chat", label: "Chat" },
                { value: "intent", label: "Intent", disabled: true },
                { value: "tools", label: "Tools" },
              ]}
              onChange={() => {}}
            />
            <SegmentedControl
              label="Selected disabled option"
              value="tools"
              options={[
                { value: "chat", label: "Chat" },
                { value: "tools", label: "Tools", disabled: true },
              ]}
              onChange={() => {}}
            />
            <SegmentedControl
              label="Disabled group"
              value="intent"
              options={SHORT_OPTIONS}
              onChange={() => {}}
              disabled
            />
            <FocusControl />
          </div>
          <div className={styles.frames}>
            <NarrowFrame width={320} />
            <NarrowFrame width={390} />
          </div>
        </div>
      </ThemeFlip>
    </section>
  );
}

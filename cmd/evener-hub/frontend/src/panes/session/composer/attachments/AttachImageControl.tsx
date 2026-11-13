import type { RefObject } from "react";
import { IconButton, Menu, Tooltip } from "../../../../widgets";
import { AttachIcon } from "./AttachIcon";
import { openImagePicker, pickerLacksCamera } from "./imagePicker";

export interface AttachImageControlProps {
  /** The surface's hidden image input; both ways of adding a picture open it. */
  inputRef: RefObject<HTMLInputElement | null>;
  testId: string;
  /** Hover text for the plain button. The menu form names its own items. */
  tooltip?: string;
}

/** The attach control of the two surfaces that stage images (the session
 * composer and the spawn prompt card). One press opens the browser's image
 * picker. Where that picker has no camera (pickerLacksCamera) the same control
 * opens a two-item menu instead, so a new photo is reachable without a second
 * button widening a control row that has no width to give on a phone. */
export function AttachImageControl({ inputRef, testId, tooltip }: AttachImageControlProps) {
  if (pickerLacksCamera()) {
    return (
      <span data-testid={testId}>
        <Menu
          icon={<AttachIcon />}
          label="Attach image"
          variant="quiet"
          items={[
            { id: "library", label: "Choose images", onSelect: () => openImagePicker(inputRef.current, "library") },
            { id: "camera", label: "Take photo", onSelect: () => openImagePicker(inputRef.current, "camera") },
          ]}
        />
      </span>
    );
  }
  const button = (
    <IconButton
      label="Attach image"
      icon={<AttachIcon />}
      variant="quiet"
      size="xs"
      type="button"
      data-testid={testId}
      onClick={() => openImagePicker(inputRef.current, "library")}
    />
  );
  return tooltip ? <Tooltip label={tooltip}>{button}</Tooltip> : button;
}

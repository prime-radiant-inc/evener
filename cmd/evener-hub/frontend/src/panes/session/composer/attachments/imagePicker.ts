// The one hidden image input each attachment surface owns serves both ways of
// adding a picture: choosing existing images, and taking a new photo. The
// `capture` attribute is what tells a phone browser to open its camera instead
// of its picker, so it is set for exactly the open that asked for the camera
// and cleared for every other, on the same element.

export type ImagePickerSource = "library" | "camera";

export function openImagePicker(input: HTMLInputElement | null, source: ImagePickerSource): void {
  if (!input) return;
  if (source === "camera") input.setAttribute("capture", "environment");
  else input.removeAttribute("capture");
  input.click();
}

/** Whether the browser's own picker for an image-only input leaves out the
 * camera, so the attach control has to offer it. That is Android: Chrome there
 * opens the system photo picker, which only browses existing pictures. iOS
 * Safari's picker already lists "Take Photo" beside the library, and a desktop
 * browser ignores `capture` altogether, so neither gains a second control. */
export function pickerLacksCamera(): boolean {
  return typeof navigator !== "undefined" && /\bAndroid\b/.test(navigator.userAgent);
}

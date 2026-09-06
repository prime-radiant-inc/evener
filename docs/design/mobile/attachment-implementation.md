# Native image composition

Product sources are the current web composer: `attachments/limits.ts`, `attachments/useAttachments.ts`, `attachments/encodePng.ts`, `attachments/textareaMarkers.ts`, and `stores/attachmentMarkers.ts`. Server input is `InputItem` in the generated protocol. The old mobile attachment service is a placeholder and is not the feature definition.

## Interaction

An attachment button lives in the composer beside model and reasoning. It opens the platform image selection surface. Accepted images appear as removable previews above the input, each identified by its stable image marker. The current web flow permits images only: no arbitrary file-upload capability is implied. Preserve the current eight-image and 8 MiB source-file limits and PNG normalization. Send, steer, queue and drain are unavailable while encoding or persistence is incomplete. Selection cancellation leaves composition untouched; decoding failure removes only that image and its marker.

## Delivery and persistence

The shared `stores/composerInput.ts` is the input boundary for web and native. Ordinary text is preserved, image-only input is allowed, attachment order and names are retained, and editing markers are translated only when submitting. Recovery retains editing anchors. The native picker must supply normalized bytes; a URI or handle is not wire image data.

Native composition must preserve selected images across hub switches and restarts, including uncertain delivery, alongside the existing per-hub/session text checkpoint. Store image bytes separately from frequently edited text so each keystroke does not rewrite up to eight base64 images. The draft record and durable submission checkpoint must refer to the same immutable image identities. Only acknowledged submission may retire its submitted image set; images added during a send must survive. Uncertain delivery never automatically replays.

## Sequence and evidence

1. Share existing web input assembly and marker translation with native. Verify structured input for image-only messages, identity gaps, ordered images, names and unchanged text. Run native checks and the canonical web gate after replacing web call sites.
2. Add native durable attachment storage and checkpoint ownership. Exercise real SQLite and filesystem boundaries, restart, hub/session isolation, failed writes, removal during pending encoding, and retained newer edits.
3. Add platform picker and PNG normalization, then the composer preview/removal flow. Verify installed iOS and Android apps with actual selected images against the isolated real Evener harness, including send/steer/queue and recovery.
4. Validate accessible naming, large text, keyboard and Back behavior, permission/cancellation handling, memory use and cleanup. Validate image rendering in the transcript as part of the end-to-end flow.

Only step 1 is implemented in this increment. There is no native attachment picker, durable image checkpoint or attachment E2E claim yet.

## Native API references

Expo's [ImagePicker](https://docs.expo.dev/versions/latest/sdk/imagepicker/) exposes the system selection UI on iOS and Android; its asset result includes optional file size and MIME type, so missing metadata requires a verified file inspection rather than assuming validity. [ImageManipulator](https://docs.expo.dev/versions/latest/sdk/imagemanipulator/) provides PNG output. Validate these against the SDK-matched installed package types before implementing the adapter. Picker cancellation and Android activity recreation need explicit handling; selection-only use does not require adding a voice or video feature.

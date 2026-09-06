# Native image composition

Product sources are the current web composer: `attachments/limits.ts`, `attachments/useAttachments.ts`, `attachments/encodePng.ts`, `attachments/textareaMarkers.ts`, and `stores/attachmentMarkers.ts`. Server input is `InputItem` in the generated protocol. The old mobile attachment service is a placeholder and is not the feature definition.

## Interaction

An attachment button lives in the composer beside model and reasoning. It opens the platform image selection surface. Accepted images appear as removable previews above the input, each identified by its stable image marker. The current web flow permits images only: no arbitrary file-upload capability is implied. Preserve the current eight-image and 8 MiB source-file limits and PNG normalization. Send, steer, queue and drain are unavailable while encoding or persistence is incomplete. Selection cancellation leaves composition untouched; decoding failure removes only that image and its marker.

## Delivery and persistence

The shared `stores/composerInput.ts` is the input boundary for web and native. Ordinary text is preserved, image-only input is allowed, attachment order and names are retained, and editing markers are translated only when submitting. Recovery retains editing anchors. The native picker must supply normalized bytes; a URI or handle is not wire image data.

Native composition must preserve selected images across hub switches and restarts, including uncertain delivery, alongside the existing per-hub/session text checkpoint. Store image bytes separately from frequently edited text so each keystroke does not rewrite up to eight base64 images. The draft record and durable submission checkpoint must refer to the same immutable image identities. Only acknowledged submission may retire its submitted image set; images added during a send must survive. Uncertain delivery never automatically replays.

## Sequence and evidence

1. Share existing web input assembly and marker translation with native. Verify structured input for image-only messages, identity gaps, ordered images, names and unchanged text. Run native checks and the canonical web gate after replacing web call sites.
2. Add native durable attachment storage and checkpoint ownership. Exercise real SQLite storage, restart, hub/session isolation, failed writes, removal during pending encoding, and retained newer edits.
3. Add platform picker and PNG normalization, then the composer preview/removal flow. Verify installed iOS and Android apps with actual selected images against the isolated real Evener harness, including send/steer/queue and recovery.
4. Validate accessible naming, large text, keyboard and Back behavior, permission/cancellation handling, memory use and cleanup. Validate image rendering in the transcript as part of the end-to-end flow.

Shared input assembly and durable image storage/checkpoint ownership are implemented. Image bytes occupy separate immutable SQLite rows; draft and uncertain image references commit with text under a savepoint. This keeps one atomic persistence boundary and avoids rewriting bytes on text edits. Removed images are deleted only when neither draft nor uncertain submission references them. Native DraftDocument supports adding/removing images, preserving failed image saves for retry, image-only submission, and preserving ordinary images during structured answers.

Native 127 tests, TypeScript and targeted Biome pass. Tests use real SQLite for close/reopen, hub isolation, byte immutability, failure rollback, uncertain recovery, newer-image retention, and cleanup. Selection tests exercise limits, cancellation, removal during encoding, and preservation of edits while encoding.

## Native picker evidence

Release builds on iPhone 17 Pro (iOS 26.5) and Pixel 7 emulator (API 35) selected the repository's icon.png fixture through the actual system photo picker. Both returned a visible image preview and editing marker. Stopping and relaunching each app retained the preview and marker in the same hub/session. Removing the image removed its marker; Android's image-only draft became empty and Send disabled, while iOS retained the pre-existing text. Android's picker supplied a generated filename; iOS retained icon.png. These are selection, normalization, and local recovery checks, not provider delivery evidence.

The picker accepts images only and normalizes to PNG. Pending tiles are removable; encoding finishes before inserting a durable editing marker, so cancellation does not leave an orphan marker. Late results after leaving the destination are ignored. Native preview tiles are a presentation adaptation of the current web attachment flow.

Native composer steering now uses current web submitRouting.ts: images or a non-empty queue select turn/drainAsSteer, carrying the observed queue revision and composed input. Plain text with an empty queue retains turn/steer. The shared store preserves its existing mutation ownership, receipt validation and failure recovery; routing stays in the native composer so the shared package does not acquire a web UI dependency.

The regression tests failed on both wrong routes before the correction. Tests exercise the real store and conversation service through a scripted wire boundary, asserting method, instance and queue guards, unchanged input, accepted receipts and retained drafts after unconfirmed delivery without retry. All 131 native tests, 358 shared store/service tests and native TypeScript pass. Both Release builds succeed. These builds do not establish provider receipt of image steering.

Empty-composer queue-only steering, native manual image delivery, transcript rendering, Android picker activity-death recovery, metadata/orientation checks, accessibility stress cases and memory/performance evidence remain outstanding. There is no native attachment delivery E2E claim yet.

## Native API references

Expo's [ImagePicker](https://docs.expo.dev/versions/latest/sdk/imagepicker/) exposes the system selection UI on iOS and Android; its asset result includes optional file size and MIME type, so missing metadata requires a verified file inspection rather than assuming validity. [ImageManipulator](https://docs.expo.dev/versions/latest/sdk/imagemanipulator/) provides PNG output. Validate these against the SDK-matched installed package types before implementing the adapter. Picker cancellation and Android activity recreation need explicit handling; selection-only use does not require adding a voice or video feature.

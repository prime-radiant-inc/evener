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

## Image send through the real hub

At source 8af2d1140, both installed Release apps selected the repository icon.png through their system picker and sent it from the composer to the isolated real hub. The session was local:034K0TIvKtyx1mAIxunSiV. The real session loop called an OpenAI-compatible scripted provider and persisted the user image and accepted communicate response. Both native transcripts displayed the provider response; the composer cleared after acknowledgement.

The provider decoded the actual image_url data, rather than inspecting the AppWire request alone. Both images were 1024 × 1024 PNGs with RGBA pixel SHA-256 `3974c3ea814e66cd13a4f1a22d4b4d6c27611d2cb8619e6bf171766248088073`, identical to the original fixture. iOS encoded 747473 bytes and Android 409093 bytes. The second provider call contained both the earlier iOS image in conversation history and the newly sent Android image. This proves ordinary image send and history retention through the selected test provider, not steering, queue delivery, every provider, or physical-device behavior.

The manual harness used test/e2e/fakellm on loopback 58877, selected by the NATIVE-IMAGE-HARNESS sentinel through the owned provider proxy. It logged only image format, dimensions, encoded length and pixel hash. Production hub 9180 was not involved.

## Transcript gallery

The current web ImageGallery is the interaction reference: thumbnails open an image viewer, and Previous/Next stay within the images attached to the same transcript item. Native now renders 112-point thumbnails and a full-screen viewer, with visible loading/failure states. Image sources preserve inline bytes, resolve relative URLs against the owning hub, and attach credentials only for that hub's origin. Tests cover source resolution, credential separation, and unsupported schemes. The existing wire projector remains the source of attachment identity and content.

All 134 native tests and TypeScript pass; both Release builds succeed. Manual iOS and Android checks displayed the real session's inline image as a thumbnail and at full size. Native inspection found an iOS modal header under the status bar; giving the modal its own SafeAreaProvider corrected it. Verified settled aspect ratio, iOS Done dismissal, and Android system Back returning to the conversation. A transition capture showed temporary stretching before the iOS viewer settled; transition polish still needs checking. Multi-image navigation, authenticated HTTP image retrieval, failed-load recovery, large text and screen-reader behavior are not yet manually verified.

## Queue-only steering and receipt correction

Native enables steering with an empty composer when the live conversation has queued input. DraftDocument.submitWithQueue durably checkpoints even an empty local payload before dispatch, retains uncertainty across remount, and blocks automatic replay. The same composer routing helper carries the observed queue revision with the drain request.

A manual iOS sequence created a real running harness session, queued icon.png, observed an empty composer and one queued entry, then pressed Steer. The server drained the queue and the provider decoded the exact fixture pixels. This exposed a receipt bug: a drain that consumes queued entries includes queueEntryIds, which the plain-steer decoder rejected. The persisted server mutation result for local:034K0xcRUdVXB4a529zcCs confirmed turn_m2, queue_1, and a pending projection receipt. Native kept the action unconfirmed rather than replaying it.

The decoder now treats drain as a distinct receipt shape, validates the consumed queue IDs when present, and preserves them in the accepted receipt. Tests reproduced the real mismatch before the correction. All 136 native tests, 358 shared store/service tests, native TypeScript and both Release builds pass. The uncertain checkpoint survived a rebuilt app relaunch; it was explicitly dismissed after provider receipt was verified. Fresh manual queue/drain acknowledgement after the decoder fix, Android queue/drain, and direct image steering still require verification.

The remaining gallery checks above, Android picker activity-death recovery, metadata/orientation checks, accessibility stress cases and memory/performance evidence remain outstanding.

## Native API references

Expo's [ImagePicker](https://docs.expo.dev/versions/latest/sdk/imagepicker/) exposes the system selection UI on iOS and Android; its asset result includes optional file size and MIME type, so missing metadata requires a verified file inspection rather than assuming validity. [ImageManipulator](https://docs.expo.dev/versions/latest/sdk/imagemanipulator/) provides PNG output. Validate these against the SDK-matched installed package types before implementing the adapter. Picker cancellation and Android activity recreation need explicit handling; selection-only use does not require adding a voice or video feature.

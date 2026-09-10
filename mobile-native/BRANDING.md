# Native branding provenance

The native app reuses the existing production Evener PWA mark byte-for-byte:
`assets/evener-icon.png` is copied from
`../cmd/evener-hub/assets/icon-512.png` (SHA-256
`e25c17d290e8302388dee529834d9fd7c7be371073581764bb2a06d5cada1133`).
`app.json` points Expo's app icon at this local copy so Expo SDK 57 can produce
the iOS AppIcon catalog during prebuild. The previous Expo fixture remains at
`assets/icon.png` for history and is no longer referenced.

The root `backgroundColor` and the `expo-splash-screen` config plugin both use
the production mark background (`#0a0a0e`). The launch screen reuses the same
canonical mark at 120 points with contain mode. Review the generated native launch screen after prebuild.
The iPad simulator icon and launch screen are recorded in
[branding evidence](../docs/design/mobile/ipad-branding-evidence.md).

`expo-splash-screen` is pinned to the Expo SDK 57 compatible `57.0.8` release;
no other dependencies were upgraded.

Expo CLI 57 defaults `clean` to true (`prebuild/index.js:109-115`), so an ordinary prebuild recreates existing native folders. The installed CLI exposes `--no-clean` and passes `clean: !args['--no-clean']` (`prebuild/index.js:66-72, 86-90, 109-113`). From `mobile-native`, use the explicit preservation option:

```bash
npx expo prebuild --no-clean --platform ios
npx expo run:ios
```

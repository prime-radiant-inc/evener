# Selected original Microsoft Comic Chat artwork

The public directory `../../public/webassets/comic/` contains exactly 12 character PNGs (six Anna, two Xeno, and one neutral Armando, Bolo, Denise, Tiki), two backdrops, six emotion icons, a manifest, an attribution record, and the unchanged upstream MIT `LICENSE`. It contains **no fonts**. These sprites and backdrops are from Microsoft's [Comic Chat release](https://github.com/microsoft/comic-chat/tree/48a162249484ab8d116c243e8203b0956d350c09), revision `48a162249484ab8d116c243e8203b0956d350c09`, not substitute or generated drawings.

`source-evidence.json` pins each of 24 retained originals by revision, upstream raw URL, Git blob SHA-1, SHA-256 and byte count (2,011,949 bytes total). Original bytes are in `source/`, except `LICENSE`, which sits with the selected public art. The historical `source/v1.0-pre/client/license.txt` differs from the current root MIT notice and is retained for context; the included artwork uses Microsoft's root MIT source release. `../../public/webassets/comic/ATTRIBUTION.md` names original art paths and code references.

The upstream `README.md` is retained byte-for-byte as `source/README.upstream.txt`. It is archival input whose relative links refer to Microsoft's repository, not maintained Evener documentation. The source manifest retains its original path and URL.

## Selected output

- `sprites/anna-{neutral,happy-wave,angry,scared,point,celebrate}.png`: original face and body combinations F0/T0, F1/T7, F5/T5, F7/T10, F16/T15, F6/T8, respectively. “Celebrate” is a descriptive label, not an original source emotion name.
- `sprites/xeno-{neutral,point}.png`: F0/T0 and F1/T3.
- `sprites/{armando,bolo,denise,tiki}-neutral.png`: first neutral face/torso source records F0/T0, F0/T0, F3/T4 and F2/T0, respectively. The upstream file paths are `v1.0-pre/client/comicart/avatars/{armando,bolo,denise,tiki}.avb`; these four newly obtained official originals are retained unchanged and pinned by hash. The record indices are verified against source emotion names, not guessed from rendered pixels.
- `backgrounds/{field,room8bs}.png`: complete 315 × 315 original BI_RLE4 BMP conversions. `emotions/fc_{neu,hap,bor,sca,ang,laf}_s.png`: complete 13 × 18 RGB BMP conversions.

`extract.py` reads AVB little-endian records using the retained `avatario.h`/`.cpp` definitions, places head relative to torso with the original offsets and native union bounds from `avatar.cpp`, then applies the nimbus, mask and ink ordering from `bodycam.cpp` without redrawing. Its `rle4.py` decodes the original backdrops including odd-width row padding. Bolo and Denise have original gray foreground bits: their composites preserve source bitwise raster operations, rather than flattening the shading into black. **Three Bolo fringe pixels** have partial bit masks (two alpha levels, 17 and 68) whose original bitwise-AND result depends on background bits. Standard PNG alpha exactly reproduces these on white and black, but cannot express the bitwise operation on every saturated backdrop; their nearest-alpha blend differs at exactly those three pixels over the verifier's blue backdrop. The verifier insists all other pixels agree.

## Offline reproduction and independent checks

Run from this directory with Python 3, Pillow, and ImageMagick `convert` installed:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 extract.py
PYTHONDONTWRITEBYTECODE=1 python3 verify.py
```

`extract.py` writes only the named 20 PNGs and `manifest.json` in the public art directory. `verify.py` needs no network or private fonts. It checks pinned original Git blob SHA-1s and SHA-256s, the unchanged MIT notice, exactly the selected output set and independently frozen PNG SHA-256s, each background and icon against ImageMagick's BMP decode, and each figure against its original foreground/mask/aura BMPs using independent bitwise Win32 raster operations on white, black and blue. Its pinned values are separate from the generated manifest and extractor. To test rejection without touching originals, pass scratch-copy roots as `--asset-root DIR --source-root DIR`; it rejects changed selected-art bytes, missing supporting sprites and missing MIT notice.

The figure source records are chosen for this web presentation. No claim is made that original Comic Chat automatically chose those precise pairings, and this is not a Windows execution of the source application. The font is sourced separately from the historical MIT release; see [`comic-fonts/ATTRIBUTION.md`](../../public/webassets/comic-fonts/ATTRIBUTION.md).

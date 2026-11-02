# Original artwork and license

The 20 PNG images in this directory are conversions of original Microsoft Comic Chat artwork from [microsoft/comic-chat](https://github.com/microsoft/comic-chat/tree/48a162249484ab8d116c243e8203b0956d350c09), pinned revision `48a162249484ab8d116c243e8203b0956d350c09`. Copyright © Microsoft Corporation. The upstream root license is MIT; its unchanged notice is preserved in [LICENSE](LICENSE). No unverified artist credit is claimed. These selected images are public assets; **Comic Sans font binaries are not included or licensed here.**

The source art is under `v1.0-pre/client/comicart/` at that revision:

- `avatars/{anna,xeno,armando,bolo,denise,tiki}.avb`: original face, torso, mask and nimbus BMP records. Four supporting-cast neutral figures use Armando F0/T0, Bolo F0/T0, Denise F3/T4 and Tiki F2/T0; indices are zero-based and each record identifies its original emotion as neutral.
- `backdrop/{field,room8bs}.bmp`: background originals.
- `avatars/fc_{neu,hap,bor,sca,ang,laf}_s.bmp`: emotion-wheel originals.

See [`../../../scripts/comic-assets/README.md`](../../../scripts/comic-assets/README.md) for selected pose pairings, conversion mechanics, verifier and the three-pixel Bolo alpha limitation. [`../../../scripts/comic-assets/source-evidence.json`](../../../scripts/comic-assets/source-evidence.json) pins upstream URLs, original file SHA-256, Git blob SHA-1 and byte counts. All 24 originals are retained unchanged in that script directory's `source/`, except the original MIT notice here. The same upstream revision's `avatario.h`, `avatario.cpp`, `avatar.h`, `avatar.cpp`, `bodycam.cpp`, `dib.h` and `dib.cpp` document the native AVB structure, offsets, drawing operations and BMP layout.

The source tree also includes an older Windows-era `v1.0-pre/client/license.txt`, retained unchanged among the source files. Its restrictions differ from the official repository's current root MIT grant and README license statement. This personal client's art extraction follows the current Microsoft Git release, not a legal determination about future redistribution. Revisit the archived notice before any separately approved release; preserve the MIT notice with the art.

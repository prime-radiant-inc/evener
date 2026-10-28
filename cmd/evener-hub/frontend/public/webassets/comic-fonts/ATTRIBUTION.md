# Comic Sans from Microsoft's Comic Chat release

`comic.ttf` is the unmodified Comic Sans MS Regular 1.20 font from Microsoft's
[Comic Chat open-source release](https://github.com/microsoft/comic-chat/commit/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17).
Evener includes it under that release's MIT license, reproduced unchanged in
[`LICENSE`](LICENSE). Comic Sans is a Microsoft trademark. The font credits
Vincent Connare and carries Microsoft's 1995 copyright notice. This use does
not imply Microsoft sponsorship.

## Exact source

- Revision: `f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17`, “Open-source release preparation.”
- Font: [`v1.0/shared/comic.ttf`](https://github.com/microsoft/comic-chat/blob/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17/v1.0/shared/comic.ttf).
- Identical second copy: `v2.1b/cchat/setup/comic.ttf`.
- Git blob: `3e2652d77c232d0f99966475137038326d70c228`.
- Size: 63,040 bytes.
- SHA-256: `08e336a641ef44f0a6c745a52c64ba10ad58a8aad631e3db79c98cb703b9893f`.
- [Release README](https://github.com/microsoft/comic-chat/blob/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17/README.md)
  explicitly licenses the project under MIT; its root
  [LICENSE](https://github.com/microsoft/comic-chat/blob/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17/LICENSE)
  has SHA-256 `722262cd04696639f0f65df38d792fb4e08a28309ff5cee15f10bdd9b66411a3`.

Only the regular face is supplied. The browser synthesizes bold from it.
No installed font, commercial webfont, conversion, subset or modified font
is included.

## Historical notices

The first archived font addition predates the root MIT license. This copy is
sourced from the later explicit MIT release, where both font paths were present.
That snapshot also retains older product EULAs at
[`v1.0/client/license.txt`](https://github.com/microsoft/comic-chat/blob/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17/v1.0/client/license.txt)
and [`v2.1b/cchat/license.txt`](https://github.com/microsoft/comic-chat/blob/f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17/v2.1b/cchat/license.txt).
Those archived notices restrict distribution and operating-system use; this
distribution relies on Microsoft's later MIT release, not those old grants.
The font's unrestricted embedding flags are technical metadata, not a separate
license.

Upstream later removed the font in commits
`347d6e0110c7de916641d02656743fcb94469d23` and
`e1e3eaf26c8c471b40c0395c8536e1a2d59d2c05`.
[Issue 15](https://github.com/microsoft/comic-chat/issues/15) discusses that
history. Its licensing assertion is a third-party comment; the issue's closure
contains no written Microsoft clarification. The source for this attribution
is the release tree and its license.

## Reproduce

In a clone of `https://github.com/microsoft/comic-chat.git`:

```sh
git show f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17:v1.0/shared/comic.ttf > comic.ttf
git show f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17:LICENSE > comic-font-LICENSE
sha256sum comic.ttf comic-font-LICENSE
```

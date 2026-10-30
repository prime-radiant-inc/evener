# Microsoft Comic Chat

Microsoft Comic Chat is a Microsoft-developed Internet Relay Chat (IRC) chat client released in 1996 that rendered conversations as automatically generated comic strips. Instead of plain text, users communicated through cartoon avatars with messages displayed in speech bubbles inside dynamically composed comic panels. The application used an expert system to determine character placement, gestures, facial expressions, balloon shape, and panel layout in real time. It shipped as part of Internet Explorer 3.0 and was later bundled with Windows 98 and MSN before being discontinued in the early 2000s.

![Comic Chat](v1.0-pre/client/readme.gif)

## About This Repository

This repository is an archive of the Comic Chat source code. It is not actively maintained.

## Table of Contents

- [Microsoft Comic Chat](#microsoft-comic-chat)
  - [About This Repository](#about-this-repository)
  - [Table of Contents](#table-of-contents)
  - [How Comic Chat Works](#how-comic-chat-works)
  - [Download Comic Chat](#download-comic-chat)
  - [IRC servers](#irc-servers)
  - [Repository Structure](#repository-structure)
    - [Version notes](#version-notes)
  - [Building](#building)
    - [Original build (Visual C++ 4.x)](#original-build-visual-c-4x)
    - [Modernized build (Visual Studio 2022)](#modernized-build-visual-studio-2022)
    - [Cloud builds](#cloud-builds)
    - [A note on the modernized folders](#a-note-on-the-modernized-folders)
    - [Original build requirements](#original-build-requirements)
  - [History](#history)
  - [License](#license)
  - [Contributing](#contributing)
  - [Trademarks](#trademarks)

## How Comic Chat Works

As users type messages, each Comic Chat client automatically determines:

- Which characters to place in each panel
- Gestures and facial expressions based on message content
- Character placement and orientation
- Word balloon shape, outline, and layout
- When to advance to a new panel
- Zoom factor for each panel

The application connects to standard IRC servers and is fully interoperable with text-based IRC clients. Non-Comic Chat users are automatically assigned characters so the entire conversation is rendered graphically.

## Download Comic Chat

If you want to jump right in and try Comic Chat without cloning the source and building it yourself, you have two options.

1. You can download Microsoft Comic Chat 2.5 from [Mermaid Elizabeth's Microsoft Chat Resources Link Page](https://mermeliz.com/). This is the version that shipped in the early 2000s.
2. You can download an [unofficial modern build](https://github.com/microsoft/comic-chat/releases/tag/unofficial-modern-builds-2026-07) of version 1.0 and version 2.5b1. These versions have been modernized with support for high-resolution monitors.

## IRC servers

Mermaid Elizabeth maintains a list of [IRC servers](https://mermeliz.com/srvr_rms.htm) that work with Comic Chat.

## Repository Structure

This repository contains source snapshots spanning the full development history of Comic Chat, from a 1996 pre-release through the 2.5 beta. It also includes two **`*-modern`** folders — worked examples that get the original code building and running on a current Windows machine (see [the note on the modernized folders](#a-note-on-the-modernized-folders) for what they are and why they exist).

| Folder | Date | Description |
|--------|------|-------------|
| [`v1.0-pre/`](v1.0-pre/) | August 1996 | Pre-release source snapshot (rup 206 "Beta 2") — [README](docs/v1.0-pre/README.md) |
| [`v1.0/`](v1.0/) | August 1996 | Comic Chat 1.0 release source — [README](docs/v1.0/README.md) |
| [`v2.1b/`](v2.1b/) | February 1998 | Comic Chat 2.1 beta source — [README](docs/v2.1b/README.md) |
| [`v2.5-beta-1/`](v2.5-beta-1/) | June 1998 | Comic Chat 2.5 beta 1 source — [README](docs/v2.5-beta-1/README.md) |
| [`artifacts/`](artifacts/) | January 1998 | SDK, companion tools, JChat, documentation |
| [`v1.0-pre-modern/`](v1.0-pre-modern/) | 2026 | Modernized v1.0-pre: builds with current Visual Studio, DPI-aware UI scaling, native TLS |
| [`v2.5-beta-1-modern/`](v2.5-beta-1-modern/) | 2026 | Modernized v2.5-beta-1: builds with current Visual Studio (nmake replaces the NT DDK build), uniform display scaling, runs live on modern IRC |
| [`docs/`](docs/) | — | Modernization write-ups and documentation |

See [`file dates.txt`](file%20dates.txt) for the original file modification timestamps from each archive.

### Version notes

- **v1.0-pre** and **v1.0** share the same internal version number (`rup 206, "Beta 2"`) but differ in ~99 of 111 shared source files. `v1.0` adds build infrastructure (`build/`, `help/`, `setup/`, `shared/`).
- **v2.1b** and **v2.5-beta-1** include an IRC protocol layer with multi-server support, OLE automation for scripting, and the Art Pack 1 character set in addition to the original characters.
- **artifacts/** contains the Comic Chat SDK, JChat (a Java client), the Betty Bot sample, xcchat, and internal design documents.

## Building

### Original build (Visual C++ 4.x)

All versions target Win32 (x86) using Visual C++ 4.x with MFC and NMAKE makefiles.

**v1.0-pre and v1.0:**
```batch
cd v1.0/client
NMAKE /f "chat.mak" CFG="chat - Win32 Release"
```

**v2.5-beta-1:**
```batch
cd v2.5-beta-1
NMAKE /f "chat.mak" CFG="chat - Win32 Release"
```

A `.mdp` (Visual C++ 4.x) project file is included in `v1.0-pre/client/` and `v1.0/client/` for IDE use.

### Modernized build (Visual Studio 2022)

`v1.0-pre-modern/` builds with a current Visual Studio C++/MFC toolchain and runs on modern high-DPI Windows:

```bat
call "<VisualStudio>\VC\Auxiliary\Build\vcvars32.bat"
cd v1.0-pre-modern
nmake /f chat.mak CFG="chat - Win32 Debug"
```

The modernization covers build fixes (legacy C++, MFC/OLE macro clashes, linker libs), **DPI-aware rendering and UI scaling**, several **UX fixes** (mouse-wheel scrolling, panels-per-row auto-fit, balloon word-wrap), and optional **native TLS** for connecting to modern IRC networks. See [`docs/MODERNIZATION.md`](docs/MODERNIZATION.md) for details.

`v2.5-beta-1-modern/` brings the more advanced **Comic Chat 2.5 beta-1** (June 1998) client — which originally built with the Windows NT DDK `BUILD.EXE` system — up on the modern toolchain with its own clean `nmake` makefile:

```bat
call "<VisualStudio>\VC\Auxiliary\Build\vcvars32.bat"
cd v2.5-beta-1-modern
nmake /f chat.mak CFG="chat - Win32 Release"    REM everyday use
nmake /f chat.mak CFG="chat - Win32 Debug"      REM asserts + TRACE for DebugView
```

It carries the mouse-wheel and panels-per-row work across and runs **DPI-unaware** so Windows scales the whole window uniformly (rather than scaling a few surfaces and leaving the rest tiny). The chief 2.5-specific fixes were dropping the MFC-4.0 common-control struct-tag remap, adding a Common Controls v6 manifest so the rebar toolbar creates, and the runtime fixes needed to connect/join/chat on a present-day IRC network. See [`v2.5-beta-1-modern/README.md`](v2.5-beta-1-modern/README.md).

### Cloud builds

The manually triggered **Build unofficial modern clients** GitHub Actions
workflow builds both modernized clients on a pinned Windows runner, packages
each executable with its art and documentation, smoke-tests the extracted ZIPs
from random folders, and uploads release-ready artifacts with SHA-256 hashes.
Fork owners can enable Actions and run the workflow without any secrets. See
[`docs/UNOFFICIAL-RELEASE.md`](docs/UNOFFICIAL-RELEASE.md) for the packaging and
manual release process.

### A note on the modernized folders

This repository is published primarily as a **historical artifact** — the source is here for reference, study, and preservation, not as a maintained product. The `*-modern` folders are **not** a polished re-release; they're **worked examples** of the kinds of changes it takes to get a 1996–1998 MFC application building and running on a current machine, such as:

- Getting it to **build with a current Visual Studio / MFC toolchain** on a normal developer machine (the original Visual C++ 4.x and NT DDK `BUILD.EXE` systems are long gone).
- **Uniform display scaling** so the window and its controls are legible on today's high-DPI monitors.
- A handful of modern-Windows compatibility fixes — Common Controls v6 for the toolbar, modern RichEdit/CRT behavior, IRC parsing that works with present-day servers, and short-circuiting the long-dead Microsoft art-download servers in favor of the bundled art.

These changes are intentionally **left as an exercise for the reader**: they demonstrate an approach and a few representative fixes rather than an exhaustive, production-hardened port. If you'd like to take it further — full per-monitor DPI awareness, TLS to modern IRC networks, the other client versions — the `*-modern` folders are a good place to start.

### Original build requirements

- Visual C++ 4.x (or compatible NMAKE toolchain)
- MFC 4.x libraries
- Windows 95 or Windows NT 4.0
- 486 processor, 8 MB RAM, 256-color video (minimum)

## History

Comic Chat was originally a Microsoft Research project developed by DJ Kurlander. The 1.0 release shipped in June 1996 bundled with Internet Explorer 3.0 and could run standalone or embedded as an OLE server within the browser. Version 2.0 shipped with Internet Explorer 4.0 and Windows 98 in 1997–1998, adding multi-server support, OLE scripting, and the Comic Chat SDK for third-party bots and extensions. The application was discontinued in the early 2000s as graphical chat gave way to instant messaging.

## License

This project is licensed under the [MIT License](LICENSE).

## Contributing

This project welcomes contributions and suggestions. Most contributions require you to agree to a
Contributor License Agreement (CLA) declaring that you have the right to, and actually do, grant us
the rights to use your contribution. For details, visit https://cla.opensource.microsoft.com.

When you submit a pull request, a CLA bot will automatically determine whether you need to provide
a CLA and decorate the PR appropriately (e.g., status check, comment). Simply follow the instructions
provided by the bot. You will only need to do this once across all repos using our CLA.

This project has adopted the [Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).
For more information see the [Code of Conduct FAQ](https://opensource.microsoft.com/codeofconduct/faq/) or
contact [opencode@microsoft.com](mailto:opencode@microsoft.com) with any additional questions or comments.

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft
trademarks or logos is subject to and must follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship.
Any use of third-party trademarks or logos are subject to those third-party's policies.
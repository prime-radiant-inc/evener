# iPhone checkpoint acceptance

A passing source test suite and an available historical TestFlight build are separate milestones. Acceptance records must identify the exact source revision, native artifact and backend revision used.

| Gate | Evidence required | Current boundary |
| --- | --- | --- |
| Reproducible source checkout | Declared dependencies install, native/shared tests and typechecking pass, web/package CI green | Native 718 tests, shared-session 673 tests and native typechecking pass locally; candidate CI remains required |
| Native dependency closure | Pure imports do not pull the web React/CSS tree into native; current-main protocol and keybinding definitions retained | Corrected closure typechecks and exports an iOS bundle; focused helper reviews pass |
| Package consumption | Outside-checkout tarball install, ESM/CommonJS runtime and declarations, shipped read-only example executes through real sockets | Independent package gate passes with a scripted external server; full SDK outcome qualification remains open |
| iPhone daily conversation | Connect, browse projects, page automatically, open/read, send/stop, handle a question or approval and return after disconnect | Current-artifact simulator journey remains required |
| Recovery and preservation | Text/image drafts, reader position and hub identity survive update, reconnect and ordinary process restart | Deterministic tests cover controllers/storage; current-artifact journey remains required |
| Current protocol behavior | Incompatible saved history remains readable; force-stop and reconnect/resume use current identities | Deterministic coverage exists; joined current-backend/native acceptance remains required |
| Distribution | Signed iPhone-only archive, exact unused build number, processed build and internal group availability | Historical build 3 is available; a reviewed checkpoint build remains required |
| Physical iPhone update | Install/update through TestFlight, connect over the intended network and retain saved app data | Open; simulator or Apple processing evidence does not close this gate |
| Release quality | Representative loading/scroll/input measurements, long streaming transcripts and full functionality matrix | Open; iPad and dedicated accessibility remain paused |

The full dated development acceptance ledger remains on branch `live-concepts-plan2-integrate` at checkpoint `04ae937af`. Its scoped receipts remain historical evidence. This file does not replace that archive or claim those journeys were repeated on the new candidate.

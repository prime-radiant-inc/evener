The warning scan regression now checks the exact unordered counts, so harmless traversal order changes do not break the test. The mobile live-row callback names its warning-field parameter clearly while preserving the wire field named `extra`.

Fixes #1939.

Validation: focused reducer and mobile warning tests, frontend and native typechecks, and formatting pass. Independent review and simplification found no findings; local RoboRev branch review 2554 passed at 214abb7d501deecd42b966b81a1fc27bd6a9db78. Based on main 9cb596336f6b33fa61606f950b99dbf3829a9222. No production changes.

The TUI recognizes marketplace removal that succeeded while clone cleanup failed. It preserves the cleanup warning, applies an authoritative snapshot when supplied, and starts reconciliation while guarding duplicate removals when the list is unavailable. Ordinary mutation errors remain retryable.

Stacked on #1940. This slice owns 167 changed production lines. Its required successor owns ordering of passive reads and asynchronous mutation results; both slices must qualify before this consumer stack lands.

Refreshed onto main `6cf3f026` by merge only. The owned patch is byte-identical to independent-reviewed `100e044b6`. Validation passed focused normal/tagged and race tests, normal/tagged/Windows vet, lint, formatting, and diff checks. Local RoboRev's known recovery-ordering dependency is resolved in the separately reviewed successor; current-head CI and complete remote raw review remain required.

# Admission CI correction review

Final candidate: `b84a87d256f3a6c00458614e723bea4121d14d2f`.
Published parent: `3c99890518646385a9cc5e1e371774357a74cca9`.

Independent Luna review accepted the behavior at `55cba1894`: the interrupt regression reaches its second scripted model response, poisons the real transcript writer, cancels the interrupt-drain context, receives ErrWriterPoisoned, and retains the queued message. The exact two-request assertion prevents a false pass through rejection before the model callback. Existing error and queue assertions remain intact.

Root compared `55cba1894..b84a87d2`: only the new tripwire explanation changed. It now accurately describes a scripted provider, local temporary transcript files, no network, and a generous hang guard. Root accepts this wording correction; there is no production or test-behavior delta. The separately tracked #1946 documentation Low was not edited.

Both CI failures were reproduced before the correction. The two named tests and related poison-writer regressions passed normally and under race/tagged execution, with agent vet/lint/format/diff checks. Exact commands are in the lane's ignored ask-boundary-1977-direct-admission-diagnosis.md. Local RoboRev2627 found no issues at the pre-wording head; final-head RoboRev2628 also found no issues. Candidate b84a87d2 is now published; fresh remote CI and raw reviews are pending.

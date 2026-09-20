The settings generation teardown test previously recorded only payload retirement, so its ordering assertion could pass even if the fence and notification cleanup ran in the wrong order. Record all three callbacks and assert fence end, notification unsubscribe, then payload retirement.

This is the focused Low follow-up to #1841. Production behavior is unchanged. The original patch passed 14 focused tests, the package TypeScript build, and local RoboRev review 2665.

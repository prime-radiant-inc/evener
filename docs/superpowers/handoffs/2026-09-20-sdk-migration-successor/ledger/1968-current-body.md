Paginated history could move retained turns across fresh turns or reverse retained runs when fragments coalesced into shared anchors. This change places retained turns within monotonic anchor bounds, preserving fresh order, retained order, and every item exactly once. Compatible transcript positions determine order within those bounds; positionless runs use the next positioned turn on either side.

For example, older B2,C4 and fresh U?,D1,C4 now produce U,D,B,C, preserving both the positionless fresh announcement and the comparable D1-before-B2 ordering. This is the private placement slice following merged #1961; public coverage/overlap classification and mobile consumers remain separate successors. Own scope: 157 changed production lines and 421 test additions.

Validation at 53ea59b003e5afe40620d2b8341c12c3efa5d654: 260 scoped tests pass; the original 49,608-case ordering matrix and its 49,608-case positionless-announcement extension both have zero failures. Production-only reversal fails the prefix regression; the prior implementation fails 35 extended numeric-order cases. Package build/qualification, frontend/native types, Biome, import checks, and independent correctness/simplification review pass. Local RoboRev #2654 passes. Current-head CI and remote raw review must settle before merge.

Review round 4 of 5. Existing coalescing Low #1967 remains separate.

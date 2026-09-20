The simplify review of #1920 at d40bf549bf3273d4154dae3eb668da692cc089b2 identified four related follow-ups in transcriptPresentation.ts: repeated tokenCounts configuration checks, flattening the derived Input/Output pair and its scope into cumulative Cached/Total fields, tuple/filter bookkeeping for four display rows, and tokenUnitLabel(undefined) where the row is explicitly whole-session.

After the history/usage stack lands, simplify this small accounting region while keeping the accepted display unchanged. Keep the derived token pair and its scope together; cumulative Cached/Total must remain whole-session. Use explicit session scope at the cumulative-label call site. Retain observable display/zero-omission tests and avoid changing a null sentinel without checking all consumers.

Do not remove pageOwnedTurnIds as dead code: the current history implementation actively queries it. No new history merge mechanism or product policy belongs in this cleanup.

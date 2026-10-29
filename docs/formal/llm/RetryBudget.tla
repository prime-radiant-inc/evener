----------------------------- MODULE RetryBudget -----------------------------
EXTENDS Naturals, Integers, Sequences
CONSTANTS Mode, MaxRetries, WallBudget, OwnedBudget, Reserve
VARIABLE s
vars == <<s>>

(* Mode is Stream (RetryStream) or Generic (Retry[T]). The effective rate-limit
   budget is the owned native-duration snapshot minus shutdown reserve, when
   positive ownership exists, otherwise the policy budget. Policy wall budget
   must still be positive to enable wall-budgeted rate limits.
   Clock advances only in Sleep, by a positive integral delay, as budgetClock
   does in retry_ratelimit_budget_test.go:10-29. No external time drift here. *)
Budget == IF OwnedBudget > 0 THEN OwnedBudget - Reserve ELSE WallBudget
Wall(err) == err = "rate" /\ WallBudget > 0
ClockMax == (IF Budget > 0 THEN Budget ELSE 0) + 2*(MaxRetries+1)
AttemptMax == (IF Budget > 0 THEN Budget ELSE 0) + MaxRetries + 1
Min(a,b) == IF a < b THEN a ELSE b
Init == s = [pc |-> "enter", attempts |-> 0, now |-> 0,
             cancelled |-> FALSE, observed |-> FALSE, err |-> "initial",
             admissions |-> <<>>, delay |-> 0]

(* Environment cancellation is not observation by the retry loop. *)
Cancel == /\ s.pc # "done" /\ ~s.cancelled
          /\ s' = [s EXCEPT !.cancelled = TRUE]

(* Stream checks ctx.Err before callback (stream_retry.go:96-100).
   Generic calls fn directly (retry_util.go:77-78). Stream precheck and entry
   are one action, not a promise that Go cancellation is atomic with entry. *)
Enter ==
    /\ s.pc = "enter"
    /\ IF Mode = "Stream" /\ s.cancelled
       THEN s' = [s EXCEPT !.pc = "done", !.observed = TRUE]
       ELSE s' = [s EXCEPT !.pc = "attempt", !.attempts = @ + 1,
                          !.admissions = Append(@,
                              [owner |-> s.err, prior |-> s.attempts,
                               time |-> s.now, observed |-> s.observed,
                               cancelled |-> s.cancelled])]

(* Abstract externally returning attempt, no hung provider, no partial output.
   Permanent includes bare context errors; ordinary includes wrapped adapter
   timeout/5xx/stream errors. Error classification itself is not modeled. *)
Return(err) == /\ s.pc = "attempt"
              /\ s' = [s EXCEPT !.pc = "check", !.err = err]

(* Both loops test success before context, then retryability and owning budget.
   Global attempts, NOT a separate ordinary-error counter, matches tests of
   an ordinary error after a rate-limit storm. *)
Check ==
    /\ s.pc = "check"
    /\ IF s.err = "success"
       THEN s' = [s EXCEPT !.pc = "done"]
       ELSE IF s.cancelled
       THEN s' = [s EXCEPT !.pc = "done", !.observed = TRUE]
       ELSE IF s.err = "permanent" \/
               (IF Wall(s.err) THEN s.now >= Budget
                ELSE s.attempts > MaxRetries)
       THEN s' = [s EXCEPT !.pc = "done"]
       ELSE s' = [s EXCEPT !.pc = "delay"]

(* Positive Retry-After of one or two ticks, MaxDelay=2, jitter disabled.
   Clip only wall-budgeted failures: retry_util.go:96-110, stream_retry:142-155.
   Nonpositive/overflow backoff and OnRetry side effects are excluded. *)
Delay(d) ==
    /\ s.pc = "delay"
    /\ s' = [s EXCEPT !.pc = "sleep",
                      !.delay = IF Wall(s.err) THEN Min(d, Budget-s.now) ELSE d]
Sleep ==
    /\ s.pc = "sleep"
    /\ IF s.cancelled
       THEN s' = [s EXCEPT !.pc = "done", !.observed = TRUE]
       ELSE s' = [s EXCEPT !.pc = "post", !.now = @ + s.delay]

(* Post-sleep wall recheck, retry_util.go:118-120, stream_retry.go:163-165.
   An ordinary retry does NOT read ctx here. Cancellation between this point
   and Enter distinguishes Generic from Stream without a noncompliant sleep. *)
PostSleep ==
    /\ s.pc = "post"
    /\ s' = [s EXCEPT !.pc = IF Wall(s.err) /\ s.now >= Budget
                            THEN "done" ELSE "enter"]
Done == /\ s.pc = "done" /\ UNCHANGED vars
Progress == Enter \/ (\E e \in {"success", "permanent", "ordinary", "rate"} : Return(e))
            \/ Check \/ (\E d \in 1..2 : Delay(d)) \/ Sleep \/ PostSleep
Next == Progress \/ Cancel \/ Done
Spec == Init /\ [][Next]_vars /\ WF_vars(Progress)

TypeOK == /\ s.pc \in {"enter", "attempt", "check", "delay", "sleep", "post", "done"}
          /\ s.attempts \in 0..AttemptMax /\ s.now \in 0..ClockMax
          /\ s.cancelled \in BOOLEAN /\ s.observed \in BOOLEAN
          /\ Len(s.admissions) = s.attempts /\ s.delay \in 0..2
NoAttemptAfterObservedCancellation ==
    \A i \in 1..Len(s.admissions) : ~s.admissions[i].observed
OwningBudgetRespected ==
    \A i \in 1..Len(s.admissions) : LET a == s.admissions[i] IN
        a.owner = "initial" \/
        (IF Wall(a.owner) THEN a.time < Budget ELSE a.prior <= MaxRetries)
(* Stronger diagnostic hypothesis, NOT the approved observed-cancellation rule. *)
NoCancelledEntry == \A i \in 1..Len(s.admissions) : ~s.admissions[i].cancelled
Terminates == <>(s.pc = "done")
=============================================================================

----------------------------- MODULE StreamSteps -----------------------------
EXTENDS Naturals, Sequences
CONSTANT MaxRounds, PublicationRule
VARIABLE s
vars == <<s>>

(* One active tool per executable response, each result identified by round.
   Usage is one additive scalar in 0..2, independent of result count.
   Local fields model goroutine locals. Public fields model StreamResult.
   Failures settle the retry group, not individual failed retry attempts. *)
Init == \E active \in BOOLEAN :
        s = [phase |-> "call", rounds |-> 0, settled |-> FALSE,
             calls |-> FALSE, active |-> active, passive |-> FALSE,
             reason |-> "stop", usage |-> 0, localResults |-> <<>>,
             localSteps |-> 0, localUsage |-> 0, publicResults |-> <<>>,
             publicSteps |-> 0, publicUsage |-> 0, unsettledExecution |-> FALSE,
             terminal |-> "none"]

(* stream_generate.go:294-318. FINISH with nil Response still produces an
   accumulator response, stream_accumulator.go:124-141,172-223. Do not invent
   an admitted missing-response branch using the test-only function seam.
   Active-tool availability is fixed by prepareGeneration, generate.go:170-230.
   Passive-call presence can vary with the calls in each model response. *)
Settle(calls, passive, reason, usage) ==
    /\ s.phase = "call"
    /\ s' = [s EXCEPT !.phase = "decide", !.settled = TRUE,
                      !.calls = calls, !.passive = passive,
                      !.reason = reason, !.usage = usage]

CanExecute == s.calls /\ s.reason \in {"tool_calls", "pause_turn"}
              /\ s.active /\ ~s.passive /\ s.rounds < MaxRounds

(* Stop predicate: lines 320-334. Execution/bookkeeping: lines 366-403.
   Atomic here because errors inside a tool become results, and no modeled
   return separates execution from local bookkeeping. *)
Execute ==
    /\ s.phase = "decide" /\ CanExecute
    /\ s' = [s EXCEPT !.phase = "boundary", !.rounds = @ + 1,
                      !.localResults = Append(@, s.rounds + 1),
                      !.localSteps = @ + 1, !.localUsage = @ + s.usage,
                      !.unsettledExecution = @ \/ ~s.settled]

(* Normal final completion copies locals, lines 336-363. *)
Final ==
    /\ s.phase = "decide" /\ ~CanExecute
    /\ s' = [s EXCEPT !.phase = "done", !.terminal = "success",
                      !.localSteps = @ + 1, !.localUsage = @ + s.usage,
                      !.publicResults = s.localResults,
                      !.publicSteps = s.localSteps + 1,
                      !.publicUsage = s.localUsage + s.usage]

(* StopWhen success copies completed locals, lines 405-419. *)
StopWhen ==
    /\ s.phase = "boundary"
    /\ s' = [s EXCEPT !.phase = "done", !.terminal = "success",
                      !.publicResults = s.localResults,
                      !.publicSteps = s.localSteps, !.publicUsage = s.localUsage]
Continue == /\ s.phase = "boundary"
            /\ s' = [s EXCEPT !.phase = "call", !.settled = FALSE]

(* Top-of-loop cancellation, lines 176-183, or terminal stepErr, 314-321:
   pre-fix writes res.err only. Fixed mode publishes completed local steps and
   usage before done closes, stream_generate.go:164-174, but never a final response. *)
Fail(kind) == /\ s.phase = "call"
              /\ s' = [s EXCEPT !.phase = "done", !.terminal = kind,
                                  !.publicResults = IF PublicationRule = "fixed"
                                                   THEN s.localResults ELSE @,
                                  !.publicSteps = IF PublicationRule = "fixed"
                                                 THEN s.localSteps ELSE @,
                                  !.publicUsage = IF PublicationRule = "fixed"
                                                 THEN s.localUsage ELSE @]
Done == /\ s.phase = "done" /\ UNCHANGED vars
Next == (\E c,p \in BOOLEAN, r \in {"stop", "tool_calls", "pause_turn"},
            u \in 0..2 : Settle(c,p,r,u))
        \/ Execute \/ Final \/ StopWhen \/ Continue
        \/ Fail("error") \/ Fail("cancelled") \/ Done
Spec == Init /\ [][Next]_vars /\ WF_vars(Next)

TypeOK == /\ s.phase \in {"call", "decide", "boundary", "done"}
          /\ s.rounds \in 0..MaxRounds
          /\ s.localResults \in Seq(1..MaxRounds)
          /\ s.publicResults \in Seq(1..MaxRounds)
          /\ s.localSteps \in 0..(MaxRounds+1)
          /\ s.publicSteps \in 0..(MaxRounds+1)
          /\ s.localUsage \in 0..(2*(MaxRounds+1))
          /\ s.publicUsage \in 0..(2*(MaxRounds+1))
RoundLimit == Len(s.localResults) <= MaxRounds
OnlySettledExecution == ~s.unsettledExecution
CompletedResultsRetained == s.phase = "done" =>
    /\ s.publicResults = s.localResults /\ s.publicSteps = s.localSteps
CompletedUsageRetained == s.phase = "done" => s.publicUsage = s.localUsage
SuccessAccounting == s.terminal = "success" =>
    /\ s.publicResults = s.localResults /\ s.publicSteps = s.localSteps
    /\ s.publicUsage = s.localUsage
Terminates == <>(s.phase = "done")
=============================================================================

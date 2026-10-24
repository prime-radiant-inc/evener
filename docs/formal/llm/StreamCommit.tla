---------------------------- MODULE StreamCommit ----------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANTS MaxAttempts, MaxEvents, Families, CommitRule
VARIABLE s
vars == <<s>>

(* StreamGenerate's public Events channel, not the agent's separate call path.
   Each content event has one nonempty payload. Starts/control carry none.
   Tool delta payload is ToolCall.Arguments, NEVER StreamEvent.Delta.
   RetryAfterPartial=false, OnReset=nil, one ordinary retryable failure class.
   Mapping: stream_generate.go:214-292, stream_retry.go:129-170. *)
Init == s = [attempt |-> 1, phase |-> "consume", events |-> <<>>,
             started |-> {}, published |-> {}, partial |-> FALSE,
             replayRisk |-> FALSE]

Start(kind) ==
    /\ s.phase = "consume" /\ Len(s.events) < MaxEvents
    /\ kind \in Families /\ kind \notin s.started
    /\ s' = [s EXCEPT !.events = Append(@, <<kind, "start">>),
                      !.started = @ \cup {kind}]

(* A Delta represents one forwarded nonempty payload. For tools it may be
   carried by TOOL_CALL_START, TOOL_CALL_DELTA, or TOOL_CALL_END; an empty
   start belongs to Start. Empty text and provider controls belong to Control.
   In pre-fix source only text set PartialOutput. Fixed source gates text,
   reasoning, tool arguments, and object payloads at stream_generate.go:257-271.
   Forwarding and classification are atomic with respect to retry admission. *)
Delta(kind) ==
    /\ s.phase = "consume" /\ Len(s.events) < MaxEvents
    /\ kind \in s.started
    /\ s' = [s EXCEPT !.events = Append(@, <<kind, "delta">>),
                      !.published = @ \cup {kind},
                      !.partial = @ \/ CommitRule = "fixed" \/ kind = "text"]

(* STREAM_START and opaque provider controls are deliberately not content.
   The model makes no claim that duplicate control events are a bug. *)
Control ==
    /\ s.phase = "consume" /\ Len(s.events) < MaxEvents
    /\ s' = [s EXCEPT !.events = Append(@, <<"control", "event">>)]

(* A stream may close early at any event count, or finish successfully.
   FINISH is buffered, not exposed as partial content, lines 241-244, 266-271. *)
Finish == /\ s.phase = "consume"
          /\ s' = [s EXCEPT !.phase = "done"]
Truncate == /\ s.phase = "consume"
            /\ s' = [s EXCEPT !.phase = "failed"]

(* No reset occurs in this caller. MaxAttempts bounds retries, not events. *)
Retry ==
    /\ s.phase = "failed" /\ ~s.partial /\ s.attempt < MaxAttempts
    /\ s' = [s EXCEPT !.attempt = @ + 1, !.phase = "consume",
                      !.events = <<>>, !.started = {}, !.partial = FALSE,
                      !.replayRisk = @ \/ s.published # {}]
Stop == /\ s.phase = "failed" /\ (s.partial \/ s.attempt = MaxAttempts)
        /\ s' = [s EXCEPT !.phase = "done"]
Done == /\ s.phase = "done" /\ UNCHANGED vars
Next == (\E kind \in Families : Start(kind) \/ Delta(kind))
        \/ Control \/ Finish \/ Truncate \/ Retry \/ Stop \/ Done
Spec == Init /\ [][Next]_vars

TypeOK == /\ s.attempt \in 1..MaxAttempts
          /\ CommitRule \in {"pre-fix", "fixed"}
          /\ s.phase \in {"consume", "failed", "done"}
          /\ Len(s.events) <= MaxEvents
          /\ s.started \subseteq Families /\ s.published \subseteq Families
          /\ s.partial \in BOOLEAN /\ s.replayRisk \in BOOLEAN
NoRetryAfterUnresetContent == ~s.replayRisk
=============================================================================

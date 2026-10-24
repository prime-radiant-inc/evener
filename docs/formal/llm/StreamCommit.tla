---------------------------- MODULE StreamCommit ----------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANTS MaxAttempts, MaxEvents, Families
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

(* Forwarding precedes setting the text-only partial flag, lines 257-261.
   Model the pair atomically: no other retry action can intervene there. *)
Delta(kind) ==
    /\ s.phase = "consume" /\ Len(s.events) < MaxEvents
    /\ kind \in s.started
    /\ s' = [s EXCEPT !.events = Append(@, <<kind, "delta">>),
                      !.published = @ \cup {kind},
                      !.partial = @ \/ kind = "text"]

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
          /\ s.phase \in {"consume", "failed", "done"}
          /\ Len(s.events) <= MaxEvents
          /\ s.started \subseteq Families /\ s.published \subseteq Families
          /\ s.partial \in BOOLEAN /\ s.replayRisk \in BOOLEAN
NoRetryAfterUnresetContent == ~s.replayRisk
=============================================================================

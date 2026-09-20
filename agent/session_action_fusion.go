package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// Action Fusion (SoL-Pi auto-research design, mechanism 1): behind the
// per-session ActionFusion flag, the file-mutation tools (edit_file,
// write_file, apply_patch) gain an optional run_after parameter — a shell
// command executed inside the same tool call after the mutation applies, with
// its output and exit status returning in the same observation. An
// edit-then-check cycle drops from three model requests to two. The model
// chooses which commands to fuse; commands that must inspect the mutation
// result first stay separate shell calls.
//
// The fused command is NOT a second execution mechanism: it is dispatched
// through the session registry's shell tool (reg.ExecuteCall), so it runs
// under the same execution environment, the session's working directory, the
// same foreground semantics (non-interactive), timeout policy, durable job
// accounting, output shaping (complete-or-handle, truncation), middleware,
// repeated-call breaker, and intent stamping as a standalone shell call.

// runAfterArgName is the optional parameter the mutation tools gain when the
// flag is on. It appears in the tool schemas (both the registry's validation
// schema and the model-advertised schema) only then; with the flag off the
// schemas stay byte-identical to a session without the mechanism.
const runAfterArgName = "run_after"

// actionFusionMutationTools lists the file-mutation tools that gain the
// optional run_after parameter under Action Fusion.
var actionFusionMutationTools = map[string]bool{
	"edit_file":   true,
	"write_file":  true,
	"apply_patch": true,
}

// mutationToolDef returns the definition a file-mutation tool registers
// under: the plain definition, plus the optional run_after parameter when
// this session runs Action Fusion (deps.actionFusion, captured once at
// registration — the flag is fixed for a session's lifetime, matching how the
// schema itself is registration-built). Flag off keeps the plain definition
// byte-identical.
func mutationToolDef(deps *toolDeps, def llm.ToolDefinition) llm.ToolDefinition {
	if deps == nil || !deps.actionFusion {
		return def
	}
	return tool.WithRunAfter(def)
}

// withActionFusionSchema is the advertising-side twin of mutationToolDef: the
// model-visible tool definitions come from the profile (rebuildToolDefsCache),
// so the run_after property is added there too, only when the flag is on.
// WithRunAfter clones the parameter schema, so the profile's shared definition
// is never mutated.
func (s *Session) withActionFusionSchema(td llm.ToolDefinition) llm.ToolDefinition {
	if !s.cfg.ActionFusion || !actionFusionMutationTools[td.Name] {
		return td
	}
	return tool.WithRunAfter(td)
}

// parseRunAfterArg validates the optional run_after argument before any
// mutation applies, so a malformed fusion cannot half-apply. Absent means no
// fused command. A non-string value is already rejected by the tool schema at
// dispatch (type "string"); the type check here is defensive so the ordering
// contract holds even for direct handler calls.
func parseRunAfterArg(args map[string]any) (string, error) {
	raw, present := args[runAfterArgName]
	if !present {
		return "", nil
	}
	command, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string command, got %T", runAfterArgName, raw)
	}
	if strings.TrimSpace(command) == "" {
		return "", errors.New(runAfterArgName + " must be a non-empty shell command")
	}
	return command, nil
}

// runAfterShellReady rejects a fused call BEFORE the mutation applies when the
// session's registry has no shell tool (a restricted role or a minimal tool
// set): run_after cannot run there, and half-applying the mutation would hide
// that from the model.
func runAfterShellReady(reg *tool.Registry) error {
	if reg == nil || reg.Get("shell") == nil {
		return errors.New(runAfterArgName + ": this session has no shell tool, so it cannot run a fused command")
	}
	return nil
}

// fuseRunAfter executes command through the registry's shell tool and returns
// the combined observation: the mutation result, then a clearly delimited
// section echoing the command and carrying the shell result's model-facing
// output verbatim — which already ends with the shell tool's bracketed footer
// carrying the exit status. The fused command's failure appears in this
// section while the mutation result stands; the caller only invokes this on a
// mutation that applied, and returns the result as a non-error observation
// either way (the model decides what to do next).
func fuseRunAfter(ctx context.Context, reg *tool.Registry, env execenv.ExecutionEnvironment, command, mutationResult string) string {
	args := map[string]any{"command": command}
	// Keep the mutation call's intent on the fused command: the registry moves
	// `intent` from args onto ctx, and the shell handler stamps it onto the
	// durable job record it creates.
	if intent := tool.IntentFromContext(ctx); intent != "" {
		args["intent"] = intent
	}
	raw, err := json.Marshal(args)
	if err != nil { // unreachable: a map of strings always marshals
		return runAfterSection(mutationResult, command, err.Error())
	}
	callID := callIDFromContext(ctx)
	if callID != "" {
		callID += "-run-after"
	}
	res := reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        callID,
		Name:      "shell",
		Type:      "function",
		Arguments: raw,
	})
	return runAfterSection(mutationResult, command, res.Output)
}

// runAfterSection renders the combined observation: the mutation result, a
// blank line, then the [run_after] header echoing the command and the shell
// result's output.
func runAfterSection(mutationResult, command, commandOutput string) string {
	var b strings.Builder
	if mutationResult != "" {
		b.WriteString(mutationResult)
		if !strings.HasSuffix(mutationResult, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	b.WriteString("[" + runAfterArgName + "] $ ")
	b.WriteString(command)
	b.WriteByte('\n')
	b.WriteString(commandOutput)
	return b.String()
}

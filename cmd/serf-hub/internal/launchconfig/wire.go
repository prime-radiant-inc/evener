package launchconfig

import "primeradiant.com/serf/appwire"

// FromWire converts an appwire.LaunchConfigLayer to the internal Layer.
func FromWire(in appwire.LaunchConfigLayer) Layer {
	out := Layer{
		Model:                       in.Model,
		FastCheapModel:              in.FastCheapModel,
		Agent:                       in.Agent,
		ReasoningEffort:             in.ReasoningEffort,
		ContextStrategy:             in.ContextStrategy,
		OpenAIResponsesContinuation: in.OpenAIResponsesContinuation,
		MaxRounds:                   copyIntPtr(in.MaxRounds),
		MaxSubagentDepth:            copyIntPtr(in.MaxSubagentDepth),
		NoProjectPrompts:            copyBoolPtr(in.NoProjectPrompts),
		NonInteractive:              copyBoolPtr(in.NonInteractive),
		AppReplaySize:               copyIntPtr(in.AppReplaySize),
		SkillsDirs:                  in.SkillsDirs,
		PluginDirs:                  in.PluginDirs,
		MCPConfigs:                  in.MCPConfigs,
		SystemPromptMode:            in.SystemPromptMode,
		SystemPromptFile:            in.SystemPromptFile,
		SystemPromptText:            in.SystemPromptText,
		SystemPromptAppendMode:      in.SystemPromptAppendMode,
		SystemPromptAppendFile:      in.SystemPromptAppendFile,
		SystemPromptAppendText:      in.SystemPromptAppendText,
		SystemPromptAppend:          in.SystemPromptAppend,
		ModelFallbacks:              copyStringSlicePtr(in.ModelFallbacks),
		Env:                         in.Env,
		Verbose:                     copyBoolPtr(in.Verbose),
		RawHTTPLogging:              copyBoolPtr(in.RawHTTPLogging),
		TraceFile:                   in.TraceFile,
		CPUProfile:                  in.CPUProfile,
		ExportATIFPath:              in.ExportATIFPath,
		ExportATIFProviderHandles:   in.ExportATIFProviderHandles,
	}
	if in.Schema != nil {
		out.Schema = *in.Schema
	}
	for _, m := range in.MCPs {
		out.MCPs = append(out.MCPs, MCPServerSpec{Name: m.Name, Command: m.Command, Args: m.Args})
	}
	return out
}

// ToWire converts an internal Layer to the appwire shape.
func ToWire(in Layer) appwire.LaunchConfigLayer {
	out := appwire.LaunchConfigLayer{
		Model:                       in.Model,
		FastCheapModel:              in.FastCheapModel,
		Agent:                       in.Agent,
		ReasoningEffort:             in.ReasoningEffort,
		ContextStrategy:             in.ContextStrategy,
		OpenAIResponsesContinuation: in.OpenAIResponsesContinuation,
		MaxRounds:                   copyIntPtr(in.MaxRounds),
		MaxSubagentDepth:            copyIntPtr(in.MaxSubagentDepth),
		NoProjectPrompts:            copyBoolPtr(in.NoProjectPrompts),
		NonInteractive:              copyBoolPtr(in.NonInteractive),
		AppReplaySize:               copyIntPtr(in.AppReplaySize),
		SkillsDirs:                  in.SkillsDirs,
		PluginDirs:                  in.PluginDirs,
		MCPConfigs:                  in.MCPConfigs,
		SystemPromptMode:            in.SystemPromptMode,
		SystemPromptFile:            in.SystemPromptFile,
		SystemPromptText:            in.SystemPromptText,
		SystemPromptAppendMode:      in.SystemPromptAppendMode,
		SystemPromptAppendFile:      in.SystemPromptAppendFile,
		SystemPromptAppendText:      in.SystemPromptAppendText,
		SystemPromptAppend:          in.SystemPromptAppend,
		ModelFallbacks:              derefStringSlicePtr(in.ModelFallbacks),
		Env:                         in.Env,
		Verbose:                     copyBoolPtr(in.Verbose),
		RawHTTPLogging:              copyBoolPtr(in.RawHTTPLogging),
		TraceFile:                   in.TraceFile,
		CPUProfile:                  in.CPUProfile,
		ExportATIFPath:              in.ExportATIFPath,
		ExportATIFProviderHandles:   in.ExportATIFProviderHandles,
	}
	if in.Schema != 0 {
		s := in.Schema
		out.Schema = &s
	}
	for _, m := range in.MCPs {
		out.MCPs = append(out.MCPs, appwire.MCPServerSpec{Name: m.Name, Command: m.Command, Args: m.Args})
	}
	return out
}

// ResolvedToWire converts an internal Resolved to the appwire shape.
func ResolvedToWire(r Resolved) appwire.LaunchConfigResolved {
	out := appwire.LaunchConfigResolved{
		Effective:  ToWire(r.Effective),
		Layers:     map[string]appwire.LaunchConfigLayer{},
		Provenance: map[string]string{},
	}
	for name, l := range r.Layers {
		out.Layers[string(name)] = ToWire(l)
	}
	for field, name := range r.Provenance {
		out.Provenance[field] = string(name)
	}
	if r.Repo != nil {
		out.Repo = &appwire.RepoLaunchConfigStatus{
			Path:    r.Repo.Path,
			Hash:    r.Repo.Hash,
			Trust:   string(r.Repo.Trust),
			Preview: r.Repo.Preview,
		}
	}
	for _, d := range r.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, appwire.LaunchConfigDiagnostic{
			Layer: string(d.Layer), Field: d.Field, Message: d.Message,
		})
	}
	return out
}

func copyIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// copyStringSlicePtr wraps a wire []string into the Layer's three-state
// pointer: nil stays nil (unset); a non-nil slice (including empty, the
// explicit-clear state) becomes a fresh non-nil pointer.
func copyStringSlicePtr(v []string) *[]string {
	if v == nil {
		return nil
	}
	out := append([]string{}, v...)
	return &out
}

// derefStringSlicePtr is the inverse of copyStringSlicePtr: nil pointer
// (unset) stays nil; a non-nil pointer becomes a non-nil slice so the wire
// carries the explicit-clear state as a non-nil empty array.
func derefStringSlicePtr(v *[]string) []string {
	if v == nil {
		return nil
	}
	return append([]string{}, (*v)...)
}

func copyBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

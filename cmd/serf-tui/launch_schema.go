package main

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

const (
	launchLayerGlobal  = "global"
	launchLayerProject = "project"
	launchLayerLaunch  = "launch"
)

type launchSchemaRowsMode string

const (
	launchSchemaRowsSettings launchSchemaRowsMode = "settings"
	launchSchemaRowsOverride launchSchemaRowsMode = "override"
)

func launchSchemaRows(schema []appwire.LaunchOption, layer appwire.LaunchConfigLayer, layerName string, mode launchSchemaRowsMode) []layerRow {
	if len(schema) == 0 {
		return nil
	}
	rows := make([]layerRow, 0, len(schema))
	for _, opt := range schema {
		switch mode {
		case launchSchemaRowsSettings:
			if !launchOptionDefaultableInLayer(opt, layerName) {
				continue
			}
		case launchSchemaRowsOverride:
			if !opt.PerLaunch {
				continue
			}
		}
		rows = append(rows, layerRowForOption(opt, layer))
	}
	return rows
}

func launchOptionDefaultableInLayer(opt appwire.LaunchOption, layerName string) bool {
	for _, layer := range opt.DefaultableLayers {
		if layer == layerName {
			return true
		}
	}
	return false
}

func layerRowForOption(opt appwire.LaunchOption, layer appwire.LaunchConfigLayer) layerRow {
	value, editValue := launchOptionValue(opt, layer)
	label := opt.Label
	if label == "" {
		label = opt.Field
	}
	return layerRow{field: opt.Field, label: label, value: value, editValue: editValue, pathCompletion: launchOptionUsesPathCompletion(opt)}
}

func launchOptionUsesPathCompletion(opt appwire.LaunchOption) bool {
	switch opt.Kind {
	case "path", "pathList":
		return opt.PathKind != ""
	default:
		return false
	}
}

func launchOptionValue(opt appwire.LaunchOption, l appwire.LaunchConfigLayer) (string, string) {
	ptrIntStr := func(p *int) string {
		if p == nil {
			return "(default)"
		}
		return fmt.Sprintf("%d", *p)
	}
	ptrBoolStr := func(p *bool) string {
		if p == nil {
			return "(default)"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	listStr := func(values []string) (string, string) {
		return fmt.Sprintf("%d entries", len(values)), strings.Join(values, ", ")
	}
	modelFallbacksStr := func(values []string) (string, string) {
		if values == nil {
			return "(default)", "(default)"
		}
		if len(values) == 0 {
			return "0 entries (explicit)", "[]"
		}
		return fmt.Sprintf("%d entries", len(values)), strings.Join(values, ", ")
	}
	switch opt.Field {
	case "agent":
		return defaultString(l.Agent), l.Agent
	case "model":
		return defaultString(l.Model), l.Model
	case "reasoning_effort":
		return defaultString(l.ReasoningEffort), l.ReasoningEffort
	case "fast_cheap_model":
		return defaultString(l.FastCheapModel), l.FastCheapModel
	case "context_strategy":
		return defaultString(l.ContextStrategy), l.ContextStrategy
	case "max_rounds":
		return ptrIntStr(l.MaxRounds), ptrIntStr(l.MaxRounds)
	case "max_subagent_depth":
		return ptrIntStr(l.MaxSubagentDepth), ptrIntStr(l.MaxSubagentDepth)
	case "no_project_prompts":
		return ptrBoolStr(l.NoProjectPrompts), ptrBoolStr(l.NoProjectPrompts)
	case "app_replay_size":
		return ptrIntStr(l.AppReplaySize), ptrIntStr(l.AppReplaySize)
	case "system_prompt_mode":
		return defaultString(l.SystemPromptMode), l.SystemPromptMode
	case "system_prompt_file":
		return defaultString(l.SystemPromptFile), l.SystemPromptFile
	case "system_prompt_text":
		return multilineSummary(l.SystemPromptText), l.SystemPromptText
	case "system_prompt_append_mode":
		return defaultString(l.SystemPromptAppendMode), l.SystemPromptAppendMode
	case "system_prompt_append_file":
		return defaultString(l.SystemPromptAppendFile), l.SystemPromptAppendFile
	case "system_prompt_append_text":
		return multilineSummary(l.SystemPromptAppendText), l.SystemPromptAppendText
	case "skills_dirs":
		return listStr(l.SkillsDirs)
	case "plugin_dirs":
		return listStr(l.PluginDirs)
	case "mcp_configs":
		return listStr(l.MCPConfigs)
	case "mcps":
		return fmt.Sprintf("%d entries (read-only)", len(l.MCPs)), ""
	case "model_fallbacks":
		return modelFallbacksStr(l.ModelFallbacks)
	case "env":
		return fmt.Sprintf("%d entries", len(l.Env)), envEditValue(l.Env)
	case "verbose":
		return ptrBoolStr(l.Verbose), ptrBoolStr(l.Verbose)
	case "trace_file":
		return defaultString(l.TraceFile), l.TraceFile
	case "cpu_profile":
		return defaultString(l.CPUProfile), l.CPUProfile
	case "export_atif_path":
		return defaultString(l.ExportATIFPath), l.ExportATIFPath
	default:
		return "(unsupported)", ""
	}
}

func defaultString(value string) string {
	if value == "" {
		return "(default)"
	}
	return value
}

func multilineSummary(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(default)"
	}
	lines := strings.Count(value, "\n") + 1
	return fmt.Sprintf("%d chars, %d lines", len(value), lines)
}

func envEditValue(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, ", ")
}

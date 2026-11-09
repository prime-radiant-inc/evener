package hub

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func voiceSpeechText(item appwire.ThreadItem, pending *appwire.PendingAskState, instance string) string {
	if item.Type == "agentMessage" {
		return item.Text
	}
	if item.Type != "commandExecution" || item.ToolName != "ask_user" || item.Status != appwire.TurnStatusCompleted || item.Error != "" || item.PrevalOnly || pending == nil || pending.Version != 1 || pending.InstanceID != instance || item.TurnID == "" || item.CallID == "" {
		return ""
	}
	if !slices.Contains(pending.Calls, appwire.PendingAskCall{TurnID: item.TurnID, CallID: item.CallID}) {
		return ""
	}
	// Raw is the successful executor's canonical public state. Sent arguments
	// can differ after repair or hooks and are never a presentation source.
	var state schema.AskUserState
	if json.Unmarshal(item.Raw, &state) != nil || state.Version != 1 || len(state.Questions) == 0 {
		return ""
	}
	var text strings.Builder
	for _, question := range state.Questions {
		if strings.TrimSpace(question.Question) == "" {
			return ""
		}
		if question.Header != "" {
			fmt.Fprintf(&text, "%s.\n", question.Header)
		}
		fmt.Fprintln(&text, question.Question)
		if question.Why != "" {
			fmt.Fprintln(&text, question.Why)
		}
		if question.MultiSelect {
			fmt.Fprintln(&text, "You can choose more than one option.")
		}
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return ""
			}
			fmt.Fprintf(&text, "%s. %s", option.Label, option.Detail)
			if option.Recommended {
				text.WriteString(" Recommended.")
			}
			text.WriteByte('\n')
		}
		if question.IfUnanswered != "" {
			fmt.Fprintf(&text, "If unanswered: %s\n", question.IfUnanswered)
		}
	}
	return strings.TrimSpace(text.String())
}

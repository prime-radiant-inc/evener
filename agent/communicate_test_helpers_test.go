package agent

import (
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

func communicateResponse(awaitReply bool, message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": awaitReply,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "communicate_test_call",
						Name:      "communicate",
						Arguments: args,
						Type:      "function",
					},
				},
			},
		},
	}
}

func finalResponse(message string) llm.Response {
	return communicateResponse(false, message)
}

func messageResponse(message string) llm.Response {
	return communicateResponse(false, message)
}

func askResponse(message string) llm.Response {
	return communicateResponse(true, message)
}

func wrapCommunicateResponse(resp llm.Response) llm.Response {
	text := strings.TrimSpace(resp.Text())
	if text == "" || len(resp.ToolCalls()) > 0 {
		return resp
	}

	awaitReply := looksLikeQuestion(text)
	wrapped := communicateResponse(awaitReply, text)
	wrapped = resp
	wrapped.Message = resp.Message
	wrapped.Message.Content = append(append([]llm.ContentPart{}, resp.Message.Content...), llm.ContentPart{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "communicate_test_call",
			Name:      "communicate",
			Arguments: communicateResponse(awaitReply, text).ToolCalls()[0].Arguments,
			Type:      "function",
		},
	})
	return wrapped
}

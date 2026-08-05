package appsource

import "encoding/json"

type codexThreadListParams struct {
	Cursor           string   `json:"cursor,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	SortKey          string   `json:"sortKey,omitempty"`
	SortDirection    string   `json:"sortDirection,omitempty"`
	SearchTerm       string   `json:"searchTerm,omitempty"`
	Statuses         []string `json:"statuses,omitempty"`
	IncludeSubagents bool     `json:"includeSubagents,omitempty"`
}

type codexThreadListResponse struct {
	Data            []codexThread `json:"data"`
	NextCursor      string        `json:"nextCursor,omitempty"`
	BackwardsCursor string        `json:"backwardsCursor,omitempty"`
}

type codexThreadReadResponse struct {
	Thread codexThread `json:"thread"`
}

type codexThreadStartResponse struct {
	Thread        codexThread `json:"thread"`
	Model         string      `json:"model"`
	ModelProvider string      `json:"modelProvider"`
}

type codexThreadResumeResponse struct {
	Thread        codexThread `json:"thread"`
	Model         string      `json:"model"`
	ModelProvider string      `json:"modelProvider"`
}

type codexThreadForkResponse struct {
	Thread        codexThread `json:"thread"`
	Model         string      `json:"model"`
	ModelProvider string      `json:"modelProvider"`
}

type codexTurnStartResponse struct {
	Turn codexTurn `json:"turn"`
}

type codexThread struct {
	ID            string            `json:"id"`
	SessionID     string            `json:"sessionId"`
	ForkedFromID  string            `json:"forkedFromId,omitempty"`
	Preview       string            `json:"preview"`
	Ephemeral     bool              `json:"ephemeral"`
	ModelProvider string            `json:"modelProvider"`
	CreatedAt     int64             `json:"createdAt"`
	UpdatedAt     int64             `json:"updatedAt"`
	Status        codexThreadStatus `json:"status"`
	Path          string            `json:"path"`
	CWD           string            `json:"cwd"`
	CLIVersion    string            `json:"cliVersion"`
	Source        string            `json:"source"`
	ThreadSource  string            `json:"threadSource,omitempty"`
	AgentNickname string            `json:"agentNickname,omitempty"`
	AgentRole     string            `json:"agentRole,omitempty"`
	Name          string            `json:"name,omitempty"`
	Turns         []codexTurn       `json:"turns,omitempty"`
}

type codexThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type codexTurn struct {
	ID          string            `json:"id"`
	Items       []json.RawMessage `json:"items,omitempty"`
	ItemsView   string            `json:"itemsView"`
	Status      string            `json:"status"`
	Error       *codexTurnError   `json:"error,omitempty"`
	StartedAt   *int64            `json:"startedAt,omitempty"`
	CompletedAt *int64            `json:"completedAt,omitempty"`
	DurationMS  *int64            `json:"durationMs,omitempty"`
}

type codexTurnError struct {
	Message           string          `json:"message"`
	AdditionalDetails string          `json:"additionalDetails,omitempty"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
}

type codexModelListResponse struct {
	Data []struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	} `json:"data"`
}

type codexModelListParams struct{}

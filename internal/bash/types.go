package bash

import "time"

type ExecuteError struct {
	Type      string   `json:"type"`
	Message   string   `json:"message"`
	Traceback []string `json:"traceback"`
	Line      *int     `json:"line,omitempty"`
	Column    *int     `json:"column,omitempty"`
}

type DisplayData struct {
	Data     map[string]string `json:"data"`
	Metadata map[string]any    `json:"metadata"`
}

type Asset struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	MIMEType  string `json:"mimeType"`
	AssetType string `json:"assetType"`
	Size      *int64 `json:"size,omitempty"`
}

type ExecuteResult struct {
	Success        bool          `json:"success"`
	Stdout         string        `json:"stdout"`
	Stderr         string        `json:"stderr"`
	Result         *string       `json:"result"`
	Error          *ExecuteError `json:"error"`
	DisplayData    []DisplayData `json:"displayData"`
	Assets         []Asset       `json:"assets"`
	ExecutionCount int           `json:"executionCount"`
	Duration       int           `json:"duration"`
}

type CompletionItem struct {
	Label         string  `json:"label"`
	InsertText    *string `json:"insertText,omitempty"`
	Kind          string  `json:"kind"`
	Detail        *string `json:"detail,omitempty"`
	Documentation *string `json:"documentation,omitempty"`
	ValuePreview  *string `json:"valuePreview,omitempty"`
	Type          *string `json:"type,omitempty"`
}

type CompleteResult struct {
	Matches     []CompletionItem `json:"matches"`
	CursorStart int              `json:"cursorStart"`
	CursorEnd   int              `json:"cursorEnd"`
	Source      string           `json:"source"`
}

type HoverResult struct {
	Found     bool    `json:"found"`
	Name      *string `json:"name,omitempty"`
	Type      *string `json:"type,omitempty"`
	Value     *string `json:"value,omitempty"`
	Signature *string `json:"signature,omitempty"`
}

type Variable struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Value      string    `json:"value"`
	Size       *string   `json:"size,omitempty"`
	Expandable bool      `json:"expandable"`
	Shape      []int     `json:"shape,omitempty"`
	DType      *string   `json:"dtype,omitempty"`
	Length     *int      `json:"length,omitempty"`
	Keys       []string  `json:"keys,omitempty"`
}

type VariablesResult struct {
	Variables []Variable `json:"variables"`
	Count     int        `json:"count"`
	Truncated bool       `json:"truncated"`
}

type VariableDetail struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Value      string     `json:"value"`
	Size       *string    `json:"size,omitempty"`
	Expandable bool       `json:"expandable"`
	Length     *int       `json:"length,omitempty"`
	FullValue  *string    `json:"fullValue,omitempty"`
	Children   []Variable `json:"children,omitempty"`
	Methods    []string   `json:"methods,omitempty"`
	Attributes []string   `json:"attributes,omitempty"`
	Truncated  bool       `json:"truncated"`
}

type IsCompleteResult struct {
	Status string `json:"status"`
	Indent string `json:"indent"`
}

type HistoryEntry struct {
	HistoryIndex int    `json:"historyIndex"`
	Code         string `json:"code"`
}

type HistoryResult struct {
	Entries []HistoryEntry `json:"entries"`
	HasMore bool           `json:"hasMore"`
}

type Environment struct {
	CWD        string  `json:"cwd"`
	Executable string  `json:"executable"`
	Shell      *string `json:"shell,omitempty"`
}

type Features struct {
	Execute        bool `json:"execute"`
	ExecuteStream  bool `json:"executeStream"`
	Interrupt      bool `json:"interrupt"`
	Complete       bool `json:"complete"`
	Inspect        bool `json:"inspect"`
	Hover          bool `json:"hover"`
	Variables      bool `json:"variables"`
	VariableExpand bool `json:"variableExpand"`
	Reset          bool `json:"reset"`
	IsComplete     bool `json:"isComplete"`
	Format         bool `json:"format"`
	History        bool `json:"history"`
	Assets         bool `json:"assets"`
}

type Capabilities struct {
	Runtime     string       `json:"runtime"`
	Version     string       `json:"version"`
	Languages   []string     `json:"languages"`
	Features    Features     `json:"features"`
	LSPFallback *string      `json:"lspFallback,omitempty"`
	Environment *Environment `json:"environment,omitempty"`
}

type StdinRequest struct {
	Prompt   string `json:"prompt"`
	Password bool   `json:"password"`
	ExecID   string `json:"exec_id"`
}

type ExecuteRequest struct {
	Code         string         `json:"code"`
	StoreHistory *bool          `json:"storeHistory,omitempty"`
	Silent       *bool          `json:"silent,omitempty"`
	AssetDir     *string        `json:"assetDir,omitempty"`
	ExecID       string         `json:"execId,omitempty"`
	CellID       *string        `json:"cellId,omitempty"`
	CellMeta     map[string]any `json:"cellMeta,omitempty"`
}

func (r ExecuteRequest) StoreHistoryValue() bool {
	if r.StoreHistory == nil {
		return true
	}
	return *r.StoreHistory
}

type CompleteRequest struct {
	Code             string  `json:"code"`
	Cursor           int     `json:"cursor"`
	TriggerKind      *string `json:"triggerKind,omitempty"`
	TriggerCharacter *string `json:"triggerCharacter,omitempty"`
}

type HoverRequest struct {
	Code   string `json:"code"`
	Cursor int    `json:"cursor"`
}

type VariablesRequest struct {
	Filter struct {
		Types          []string `json:"types,omitempty"`
		NamePattern    string   `json:"namePattern,omitempty"`
		ExcludePrivate *bool    `json:"excludePrivate,omitempty"`
	} `json:"filter,omitempty"`
}

type VariableDetailRequest struct {
	Path           []string `json:"path,omitempty"`
	MaxChildren    *int     `json:"maxChildren,omitempty"`
	MaxValueLength *int     `json:"maxValueLength,omitempty"`
}

type IsCompleteRequest struct {
	Code string `json:"code"`
}

type FormatRequest struct {
	Code string `json:"code"`
}

type HistoryRequest struct {
	N       *int    `json:"n,omitempty"`
	Pattern *string `json:"pattern,omitempty"`
	Before  *int    `json:"before,omitempty"`
}

type InputRequest struct {
	ExecID string `json:"exec_id"`
	Text   string `json:"text"`
}

type SSEEvent struct {
	Type string
	Data any
}

type WorkerInfo struct {
	CWD            string    `json:"cwd"`
	BashPath       string    `json:"bashPath"`
	ExecutionCount int       `json:"executionCount"`
	Created        time.Time `json:"created"`
	LastActivity   time.Time `json:"lastActivity"`
	Running        bool      `json:"running"`
}

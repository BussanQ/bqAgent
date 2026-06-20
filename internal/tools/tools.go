package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type JSONSchemaProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type JSONSchema struct {
	Type       string                        `json:"type"`
	Properties map[string]JSONSchemaProperty `json:"properties"`
	Required   []string                      `json:"required,omitempty"`
}

type FunctionDefinition struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
	// RawParameters, when set, is sent to the model as the "parameters" JSON
	// schema verbatim instead of the structured Parameters field. It lets MCP
	// tools (whose input schemas are arbitrary nested JSON) pass through
	// unchanged. Builtin tools leave it nil and keep using Parameters.
	RawParameters json.RawMessage `json:"-"`
}

// MarshalJSON emits "parameters" from RawParameters when present, otherwise
// from the structured Parameters. This is the only place tool serialization
// diverges between builtin and MCP-sourced definitions.
func (f FunctionDefinition) MarshalJSON() ([]byte, error) {
	type alias struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	out := alias{Name: f.Name, Description: f.Description}
	if len(f.RawParameters) > 0 {
		out.Parameters = f.RawParameters
	} else {
		encoded, err := json.Marshal(f.Parameters)
		if err != nil {
			return nil, err
		}
		out.Parameters = encoded
	}
	return json.Marshal(out)
}

type Definition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type Function func(ctx context.Context, args map[string]any) (string, error)

type Options struct {
	WorkspaceRoot  string
	IncludePlan    bool
	SearchProvider string
	SearchAPIKey   string
	SearchBaseURL  string
	MemoryDir      string
	// Todos backs the todo_write tool. When nil a fresh store is created so the
	// tool still works (its list is just not shared with the caller).
	Todos *TodoStore
	// ExtraDefinitions / ExtraFunctions let an outside package (e.g. MCP) inject
	// additional tools into the catalog without tools importing it. Builtin
	// tools win on name conflict.
	ExtraDefinitions []Definition
	ExtraFunctions   map[string]Function
}

type Catalog struct {
	definitions []Definition
	functions   map[string]Function
}

func Definitions() []Definition {
	return cloneDefinitions(builtinDefinitions())
}

func Registry() map[string]Function {
	return RegistryWithOptions(Options{})
}

func RegistryWithOptions(options Options) map[string]Function {
	searchProvider := firstConfigured(options.SearchProvider, searchProviderFromEnv())
	searchAPIKey := firstConfigured(options.SearchAPIKey, searchAPIKeyFromEnv())
	searchBaseURL := firstConfigured(options.SearchBaseURL, searchBaseURLFromEnv())
	todoStore := options.Todos
	if todoStore == nil {
		todoStore = NewTodoStore()
	}
	registry := map[string]Function{
		"execute_bash":  ExecuteBashInDir(options.WorkspaceRoot),
		"read_file":     ReadFileFromRoot(options.WorkspaceRoot),
		"write_file":    WriteFileToRoot(options.WorkspaceRoot),
		"edit_file":     EditFileInRoot(options.WorkspaceRoot),
		"grep":          GrepInRoot(options.WorkspaceRoot),
		"glob":          GlobInRoot(options.WorkspaceRoot),
		"todo_write":    TodoWriteWithStore(todoStore),
		"web_search":    WebSearchWithProviderConfig(searchProvider, searchAPIKey, searchBaseURL),
		"web_fetch":     WebFetch,
		"install_skill": InstallSkillToRoot(options.WorkspaceRoot),
		"mem_save":      MemSaveInDir(options.MemoryDir),
		"mem_get":       MemGetInDir(options.MemoryDir),
	}
	for name, function := range options.ExtraFunctions {
		if _, exists := registry[name]; exists {
			continue // builtin tools win on name conflict
		}
		registry[name] = function
	}
	return registry
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func NewCatalog(options Options) Catalog {
	definitions := Definitions()
	if options.IncludePlan {
		definitions = append(definitions, PlanDefinition())
	}
	existing := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		existing[definition.Function.Name] = struct{}{}
	}
	for _, definition := range options.ExtraDefinitions {
		if _, clash := existing[definition.Function.Name]; clash {
			continue // builtin tools win on name conflict
		}
		definitions = append(definitions, definition)
		existing[definition.Function.Name] = struct{}{}
	}
	return Catalog{
		definitions: definitions,
		functions:   RegistryWithOptions(options),
	}
}

func (catalog Catalog) Definitions() []Definition {
	return cloneDefinitions(catalog.definitions)
}

func (catalog Catalog) Registry() map[string]Function {
	cloned := make(map[string]Function, len(catalog.functions))
	for name, function := range catalog.functions {
		cloned[name] = function
	}
	return cloned
}

func PlanDefinition() Definition {
	return Definition{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "plan",
			Description: "Break down complex task into steps and execute sequentially",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchemaProperty{
					"task": {Type: "string"},
				},
				Required: []string{"task"},
			},
		},
	}
}

func builtinDefinitions() []Definition {
	return []Definition{
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "execute_bash",
				Description: "Execute a bash command",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"command": {Type: "string"},
					},
					Required: []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "read_file",
				Description: "Read a file. Optionally pass offset (1-based start line) and limit (number of lines) to read part of a large file.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"path":   {Type: "string"},
						"offset": {Type: "string", Description: "Optional 1-based start line"},
						"limit":  {Type: "string", Description: "Optional number of lines to read"},
					},
					Required: []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "write_file",
				Description: "Write to a file (overwrites the whole file). For changing part of an existing file, prefer edit_file.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"path":    {Type: "string"},
						"content": {Type: "string"},
					},
					Required: []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "edit_file",
				Description: "Replace an exact string in a file. old_string must match exactly once unless replace_all is true. Far more efficient than rewriting the whole file.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"path":        {Type: "string"},
						"old_string":  {Type: "string", Description: "Exact text to replace (include surrounding context to make it unique)"},
						"new_string":  {Type: "string", Description: "Replacement text"},
						"replace_all": {Type: "string", Description: "Optional true/false; replace every occurrence. Defaults to false."},
					},
					Required: []string{"path", "old_string", "new_string"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "grep",
				Description: "Search file contents by regular expression (Go regexp). Returns path:line:text. Skips .git and binary files.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"pattern":     {Type: "string", Description: "Go regexp to search for"},
						"path":        {Type: "string", Description: "Optional file or directory to search (defaults to the workspace root)"},
						"glob":        {Type: "string", Description: "Optional filename filter, e.g. *.go"},
						"ignore_case": {Type: "string", Description: "Optional true/false for case-insensitive search"},
						"max_results": {Type: "string", Description: "Optional cap on the number of matching lines"},
					},
					Required: []string{"pattern"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "glob",
				Description: "Find files by glob pattern (supports ** for any depth, e.g. **/*.go). Returns paths, most-recently-modified first.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"pattern": {Type: "string", Description: "Glob pattern, e.g. **/*.go"},
						"path":    {Type: "string", Description: "Optional base directory (defaults to the workspace root)"},
					},
					Required: []string{"pattern"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "todo_write",
				Description: "Create or update the task list for the current work. Pass todos as a JSON array string of {content, status, activeForm}, status in pending|in_progress|completed. Keep one item in_progress at a time.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"todos": {Type: "string", Description: "JSON array of {content, status, activeForm}"},
					},
					Required: []string{"todos"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for up-to-date information via Tavily. Requires SEARCH_API_KEY; Firecrawl env vars are supported as a compatibility fallback.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"query": {Type: "string", Description: "The search query"},
					},
					Required: []string{"query"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "web_fetch",
				Description: "Fetch content from a web URL",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"url":          {Type: "string", Description: "The URL to fetch"},
						"extract_mode": {Type: "string", Description: "Optional extraction mode: markdown (default) or text"},
						"max_chars":    {Type: "string", Description: "Optional positive integer limit for extracted content length"},
					},
					Required: []string{"url"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "install_skill",
				Description: "Install a workspace skill from a URL into .agent/skills/<name>/SKILL.md.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"url":       {Type: "string", Description: "The URL to fetch skill markdown or readable skill instructions from"},
						"name":      {Type: "string", Description: "Optional skill id; derived from the URL when omitted"},
						"overwrite": {Type: "string", Description: "Optional true/false string; defaults to false"},
					},
					Required: []string{"url"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "mem_save",
				Description: "Save knowledge to memory. Use target=\"longterm\" for durable facts, preferences, and patterns. Use target=\"daily\" for session notes and task context.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"target":  {Type: "string", Description: "Where to save: \"daily\" or \"longterm\""},
						"content": {Type: "string", Description: "The knowledge or note to save"},
					},
					Required: []string{"target", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "mem_get",
				Description: "Read memory contents. Use to recall saved knowledge and context.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"target": {Type: "string", Description: "Which memory to read: \"daily\", \"longterm\", or \"yesterday\""},
					},
					Required: []string{"target"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "run_skill",
				Description: "Execute a workspace skill when one of the loaded skills is relevant to the task.",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"skill": {Type: "string", Description: "Workspace skill id from the loaded skills list"},
						"args":  {Type: "string", Description: "Optional raw arguments or task details passed to the skill"},
					},
					Required: []string{"skill"},
				},
			},
		},
	}
}

func cloneDefinitions(definitions []Definition) []Definition {
	cloned := make([]Definition, len(definitions))
	copy(cloned, definitions)
	return cloned
}

func requireString(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return text, nil
}

func requireStringAlias(args map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("argument %q must be a string", key)
		}
		return text, nil
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("missing required string argument")
	}
	if len(keys) == 1 {
		return "", fmt.Errorf("missing required argument %q", keys[0])
	}
	return "", fmt.Errorf("missing required argument %q (or %q)", keys[0], keys[1])
}

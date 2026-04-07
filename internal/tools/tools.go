package tools

import (
	"context"
	"fmt"
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
}

type Definition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type Function func(ctx context.Context, args map[string]any) (string, error)

type Options struct {
	WorkspaceRoot string
	IncludePlan   bool
	SearchAPIKey  string
	SearchBaseURL string
	MemoryDir     string
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
	return map[string]Function{
		"execute_bash": ExecuteBashInDir(options.WorkspaceRoot),
		"read_file":    ReadFileFromRoot(options.WorkspaceRoot),
		"write_file":   WriteFileToRoot(options.WorkspaceRoot),
		"web_search":   WebSearchWithConfig(options.SearchAPIKey, options.SearchBaseURL),
		"web_fetch":    WebFetch,
		"mem_save":     MemSaveInDir(options.MemoryDir),
		"mem_get":      MemGetInDir(options.MemoryDir),
	}
}

func NewCatalog(options Options) Catalog {
	definitions := Definitions()
	if options.IncludePlan {
		definitions = append(definitions, PlanDefinition())
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
				Description: "Read a file",
				Parameters: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"path": {Type: "string"},
					},
					Required: []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "write_file",
				Description: "Write to a file",
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
				Name:        "web_search",
				Description: "Search the web for up-to-date information",
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

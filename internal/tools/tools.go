package tools

import "fmt"

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

type Function func(args map[string]any) (string, error)

func Definitions() []Definition {
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
	}
}

func Registry() map[string]Function {
	return map[string]Function{
		"execute_bash": ExecuteBash,
		"read_file":    ReadFile,
		"write_file":   WriteFile,
	}
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

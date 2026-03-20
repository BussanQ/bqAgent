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

type Options struct {
	WorkspaceRoot string
	IncludePlan   bool
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

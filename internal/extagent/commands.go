package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
)

// NativeCommand contains an agent-reported completion and the exact text its
// prompt protocol accepts. A Codex skill path stays on the server.
type NativeCommand struct {
	Name, Description, Hint, Text, Kind string
	skillPath                           string
}

func validCommandName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.Contains(name, "/") &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

type commandInfo struct {
	Name, Description, ArgumentHint, Source string
	Input                                   *struct{ Hint string }
}

func slashCommandRows(rows []commandInfo) []NativeCommand {
	var commands []NativeCommand
	seen := make(map[string]bool)
	for _, row := range rows {
		name := strings.TrimPrefix(row.Name, "/")
		if !validCommandName(name) || seen[name] {
			continue
		}
		seen[name] = true
		hint := row.ArgumentHint
		if row.Input != nil {
			hint = firstNonEmpty(row.Input.Hint, hint)
		}
		commands = append(commands, NativeCommand{Name: name, Text: "/" + name, Description: row.Description, Hint: hint, Kind: row.Source})
	}
	return commands
}

func (broker *Broker) Commands(ctx context.Context, name AgentName, bqID, cwd string) ([]NativeCommand, error) {
	t, state, err := broker.catalogTransport(ctx, name, bqID)
	if err != nil {
		return nil, err
	}
	rows, err := broker.reportedCommands(ctx, t, state, bqID, cwd)
	rows = compactCatalog(t, rows)
	if supportsInteractionMode(name, t.Kind) {
		inventory := broker.recentInventory(t, cwd)
		if inventory.modes == nil || slices.Contains(inventory.modes, "plan") || inventory.modeListMissing && state.InteractionMode != "" {
			rows = slices.DeleteFunc(rows, func(row NativeCommand) bool { return row.Text == "/plan" })
			rows = append([]NativeCommand{{Name: "plan", Text: "/plan", Kind: "control", Description: "将下一条消息切换到规划模式"}}, rows...)
		}
	}
	return rows, err
}

func (broker *Broker) reportedCommands(ctx context.Context, t *AgentTransport, state SessionState, bqID, cwd string) ([]NativeCommand, error) {
	name := t.Agent
	broker.mu.Lock()
	active := broker.acpClients[acpClientKey{sessionID: bqID, agent: name}]
	broker.mu.Unlock()
	switch t.Kind {
	case TransportAppServer:
		if c, ok := active.(*codexAppServerClient); ok {
			return readCodexSkills(ctx, c.request, cwd)
		}
		client, err := NewCodexAppServerClient(ctx, t.Command, cwd)
		if err != nil {
			return nil, err
		}
		defer client.Close()
		if err = client.Initialize(ctx); err != nil {
			return nil, err
		}
		return readCodexSkills(ctx, client.(*codexAppServerClient).request, cwd)
	case TransportACP:
		return broker.acpCommands(ctx, t, active, state.ExternalSessionID, cwd)
	case TransportCLI:
		if name == AgentPi || name == AgentClaude {
			return cliCommands(ctx, t, cwd)
		}
	}
	return nil, nil
}

func readCodexSkills(ctx context.Context, rpc func(context.Context, string, any) (rpcEnvelope, error), cwd string) ([]NativeCommand, error) {
	msg, err := rpc(ctx, "skills/list", map[string]any{"cwds": []string{cwd}})
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []struct {
			Cwd    string
			Skills []struct {
				Name, Path, ShortDescription, Description string
				Enabled                                   *bool
				Interface                                 struct{ ShortDescription string }
			}
		}
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		return nil, err
	}
	for _, entry := range result.Data {
		if entry.Cwd != cwd {
			continue
		}
		var commands []NativeCommand
		seen := make(map[string]bool)
		for _, skill := range entry.Skills {
			if !validCommandName(skill.Name) || skill.Path == "" || skill.Enabled != nil && !*skill.Enabled || seen[skill.Name] {
				continue
			}
			seen[skill.Name] = true
			commands = append(commands, NativeCommand{Name: skill.Name, Kind: "skill", Text: "$" + skill.Name, skillPath: skill.Path, Description: firstNonEmpty(skill.Interface.ShortDescription, skill.ShortDescription, skill.Description)})
		}
		return commands, nil
	}
	return nil, nil
}

// Re-read the executing child's report before attaching a skill path. Older
// agents and removed skills still receive Codex's plain $name mention.
func (c *codexAppServerClient) pickedSkill(ctx context.Context, prompt string) map[string]any {
	mention := strings.TrimSpace(prompt)
	if !strings.HasPrefix(mention, "$") || c.cmd == nil {
		return nil
	}
	if i := strings.IndexFunc(mention, unicode.IsSpace); i >= 0 {
		mention = mention[:i]
	}
	if !validCommandName(mention[1:]) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	skills, _ := readCodexSkills(ctx, c.request, c.cmd.Dir)
	for _, skill := range skills {
		if skill.Text == mention {
			return map[string]any{"type": "skill", "name": skill.Name, "path": skill.skillPath}
		}
	}
	return nil
}

func cliCommands(ctx context.Context, t *AgentTransport, cwd string) ([]NativeCommand, error) {
	if t.Agent == AgentClaude && claudeCommandsDisabled(t.Command.Args) {
		return nil, nil
	}
	var rows []NativeCommand
	args := catalogCLIArgs(t)
	err := runCatalogProcess(ctx, t.Command.Command, args, cwd, func(stdin io.WriteCloser, stdout io.ReadCloser) error {
		cmd := `{"id":"commands","type":"get_commands"}`
		if t.Agent == AgentClaude {
			cmd = `{"type":"control_request","request_id":"commands","request":{"subtype":"initialize"}}`
		}
		err := readCatalogControl(ctx, t.Agent, cmd, stdin, stdout, func(line []byte) (bool, error) {
			result, found, err := parseCLICommandReply(t.Agent, line)
			if found {
				rows = result
			}
			return found, err
		})
		if err == io.EOF {
			return fmt.Errorf("%s 未返回命令目录", t.Agent)
		}
		return err
	})
	return rows, err
}

func claudeCommandsDisabled(args []string) bool {
	if sep := commandWrapperSeparator(args); sep >= 0 {
		args = args[sep+2:]
	}
	return hasArg(args, "--disable-slash-commands")
}

func parseCLICommandReply(name AgentName, line []byte) ([]NativeCommand, bool, error) {
	var frame struct {
		Type, ID, Command, Error string
		Success                  bool
		Data                     struct{ Commands []commandInfo }
		Response                 struct {
			ID             string `json:"request_id"`
			Subtype, Error string
			Response       struct{ Commands []commandInfo }
		}
	}
	if json.Unmarshal(line, &frame) != nil {
		return nil, false, nil
	}
	switch name {
	case AgentPi:
		if frame.Type != "response" || frame.ID != "commands" || frame.Command != "get_commands" {
			return nil, false, nil
		}
		if !frame.Success {
			return nil, true, fmt.Errorf("Pi 命令目录：%s", frame.Error)
		}
		return slashCommandRows(frame.Data.Commands), true, nil
	case AgentClaude:
		if frame.Type != "control_response" || frame.Response.ID != "commands" {
			return nil, false, nil
		}
		if frame.Response.Subtype != "success" {
			return nil, true, fmt.Errorf("Claude 命令目录：%s", frame.Response.Error)
		}
		return slashCommandRows(frame.Response.Response.Commands), true, nil
	}
	return nil, false, nil
}

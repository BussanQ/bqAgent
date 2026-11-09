package extagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Preserve wrapper arguments and workspace/provider settings, replacing only
// the flags whose values belong to the catalog probe or to the chosen model.
func withoutCLIOptions(args []string, names ...string) []string {
	var result []string
	start := 0
	if sep := commandWrapperSeparator(args); sep >= 0 {
		start = sep + 2
	}
	result = append(result, args[:start]...)
	for i := start; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		if slices.Contains(names, flag) {
			if !inline {
				i++
			}
			continue
		}
		result = append(result, args[i])
	}
	return result
}

func catalogCLIArgs(t *AgentTransport) []string {
	args := t.Command.Args
	switch t.Agent {
	case AgentClaude:
		args = append(withoutCLIOptions(args, "--input-format", "--output-format"), "--input-format", "stream-json", "--output-format", "stream-json")
		if !hasArg(args, "-p") && !hasArg(args, "--print") {
			args = append(args, "-p")
		}
		for _, flag := range []string{"--verbose", "--no-session-persistence"} {
			if !hasArg(args, flag) {
				args = append(args, flag)
			}
		}
	case AgentPi:
		args = append(withoutCLIOptions(withoutPiSessionFlags(args), "--mode"), "--mode", "rpc", "--no-session")
	case AgentCodex:
		args = codexCatalogArgs(args)
	}
	return args
}

func cliModelCatalog(ctx context.Context, t *AgentTransport, cwd string) (ModelCatalog, error) {
	var menu ModelCatalog
	args := catalogCLIArgs(t)
	var control string
	switch t.Agent {
	case AgentClaude:
		control = claudeModelControlRequests
	case AgentPi:
		control = `{"type":"get_state","id":"state"}
{"type":"get_available_models","id":"models"}`
	case AgentCodex:
		// debug models writes JSON without a control request.
	default:
		return menu, fmt.Errorf("%s 没有 CLI 模型目录", t.Agent)
	}
	err := runCatalogProcess(ctx, t.Command.Command, args, cwd, func(stdin io.WriteCloser, stdout io.ReadCloser) error {
		return readCLIModels(ctx, t.Agent, control, stdin, stdout, &menu)
	})
	return menu, err
}

func runCatalogProcess(ctx context.Context, command string, args []string, cwd string, read func(io.WriteCloser, io.ReadCloser) error) error {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, command, args...)
	cmd.Dir = cwd
	configureExternalProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}
	defer func() { cancel(); stdin.Close(); cmd.Wait() }()
	return read(stdin, stdout)
}

func readCLIModels(ctx context.Context, name AgentName, control string, stdin io.WriteCloser, stdout io.ReadCloser, menu *ModelCatalog) error {
	if name == AgentClaude {
		return readClaudeModelCatalog(ctx, control, stdin, stdout, menu)
	}
	if name == AgentCodex {
		var data struct {
			Models []struct {
				Slug       string
				Name       string `json:"display_name"`
				Visibility string
				Default    string                    `json:"default_reasoning_level"`
				Levels     []struct{ Effort string } `json:"supported_reasoning_levels"`
			}
		}
		if err := json.NewDecoder(io.LimitReader(stdout, 16<<20)).Decode(&data); err != nil {
			return err
		}
		for _, row := range data.Models {
			if row.Visibility == "" || row.Visibility == "list" {
				menu.add(row.Slug, row.Name)
				opts := EffortMenu{Default: row.Default}
				for _, level := range row.Levels {
					opts.add(level.Effort, "")
				}
				menu.setEfforts(row.Slug, opts)
			}
		}
		return nil
	}
	var gotState, gotList bool
	var stateWait *time.Timer
	err := readCatalogControl(ctx, name, control, stdin, stdout, func(line []byte) (bool, error) {
		if parsePiModelState(line, menu) {
			gotState = true
		}
		found, err := parsePiModelReply(line, menu)
		gotList = gotList || found
		// The inventory can arrive before Pi reports its configured default.
		// Bound this optional read even if the probe keeps its RPC stream open.
		if found && err == nil && !gotState && stateWait == nil {
			stateWait = time.AfterFunc(250*time.Millisecond, func() { _ = stdout.Close() })
		}
		return gotList && gotState, err
	})
	if stateWait != nil {
		stateWait.Stop()
		if ctx.Err() == nil && (errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)) {
			return nil
		}
	}
	if gotList && err == io.EOF {
		return nil
	}
	if err == io.EOF {
		return fmt.Errorf("%s 未返回模型目录", name)
	}
	return err
}

func readCatalogControl(ctx context.Context, name AgentName, control string, stdin io.WriteCloser, stdout io.Reader, read func([]byte) (bool, error)) error {
	if _, err := io.WriteString(stdin, control+"\n"); err != nil {
		return err
	}
	if name == AgentClaude {
		if err := stdin.Close(); err != nil {
			return err
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), maxACPProtocolMessageBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if name == AgentPi {
			var event struct {
				Type, Method string
				ID           piRequestID
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "extension_ui_request" {
				switch event.Method {
				case "select", "confirm", "input", "editor":
					// This is a metadata-only process with no conversation. Do
					// not block the catalog on an extension's startup dialog.
					if event.ID != (piRequestID{}) {
						if err := json.NewEncoder(stdin).Encode(map[string]any{"type": "extension_ui_response", "id": event.ID.value(), "cancelled": true}); err != nil {
							return err
						}
					}
				}
				continue
			}
		}
		if found, err := read(line); found || err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanner.Err() != nil {
		return scanner.Err()
	}
	return io.EOF
}

func parsePiModelState(line []byte, menu *ModelCatalog) bool {
	var frame struct {
		ID, Type, Command string
		Success           bool
		Data              struct {
			Model         piModel
			ThinkingLevel string
		}
	}
	if json.Unmarshal(line, &frame) != nil || frame.ID != "state" || frame.Type != "response" || frame.Command != "get_state" {
		return false
	}
	if model := frame.Data.Model.optionID(); frame.Success && model != "" {
		menu.DefaultModel = model
		menu.SelectedEffort = frame.Data.ThinkingLevel
	}
	return true
}

func piCatalogModelID(provider, model string) string {
	if strings.Contains(provider, "/") || validateModel(provider) != nil || validateModel(model) != nil {
		return ""
	}
	id := provider + "/" + model
	if validateModel(id) != nil {
		return ""
	}
	return id
}

func codexCatalogArgs(args []string) []string {
	// Keep wrapper and global options. An exec-only flag such as --json
	// isn't valid for debug models, but profile/provider overrides still apply.
	start := 0
	if separator := commandWrapperSeparator(args); separator >= 0 {
		start = separator + 2
	}
	result := slices.Clone(args[:start])
	for i := start; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		switch flag {
		case "-c", "--config", "-p", "--profile", "--enable", "--disable":
			result = append(result, args[i])
			if !inline && i+1 < len(args) {
				i++
				result = append(result, args[i])
			}
		}
	}
	return append(result, "debug", "models")
}

func parsePiModelReply(line []byte, menu *ModelCatalog) (bool, error) {
	var frame struct {
		ID, Type, Command, Error string
		Success                  bool
		Data                     struct {
			Models []struct {
				ID, Provider, Name string
				Reasoning          bool
				ThinkingLevelMap   map[string]*string
			}
		}
	}
	if json.Unmarshal(line, &frame) != nil || frame.Type != "response" || frame.Command != "get_available_models" || frame.ID != "models" {
		return false, nil
	}
	if !frame.Success {
		return true, fmt.Errorf("Pi 模型目录：%s", frame.Error)
	}
	for _, row := range frame.Data.Models {
		if id := piCatalogModelID(row.Provider, row.ID); id != "" {
			menu.add(id, row.Provider+" / "+firstNonEmpty(row.Name, row.ID))
			var opts EffortMenu
			if row.Reasoning {
				for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
					mapped, known := row.ThinkingLevelMap[level]
					if known && mapped == nil || !known && (level == "xhigh" || level == "max") {
						continue
					}
					opts.add(level, "")
				}
			}
			menu.setEfforts(id, opts)
		}
	}

	return true, nil
}

type claudeListedModel struct {
	Value, DisplayName, ResolvedModel string
	Disabled, SupportsEffort          bool
	SupportedEffortLevels             []string
}

func addClaudeModels(menu *ModelCatalog, rows []claudeListedModel) {
	for _, row := range rows {
		if row.Disabled || validateModel(row.Value) != nil {
			continue
		}
		var opts EffortMenu
		if row.SupportsEffort {
			for _, level := range row.SupportedEffortLevels {
				opts.add(level, "")
			}
		}
		// Claude may return several aliases for one version. Keep both the
		// concrete version (which stays pinned) and each selectable alias.
		if row.ResolvedModel != "default" && validateModel(row.ResolvedModel) == nil {
			menu.add(row.ResolvedModel, row.ResolvedModel)
			if existing, found := menu.Efforts[row.ResolvedModel]; !found || row.Value == row.ResolvedModel ||
				(row.Value != "default" && len(existing.Options) == 0 && len(opts.Options) > 0) {
				menu.setEfforts(row.ResolvedModel, opts)
			}
			if row.Value == "default" {
				menu.DefaultModel = row.ResolvedModel
			}
		}
		if row.Value != "default" {
			menu.add(row.Value, row.DisplayName)
			menu.setEfforts(row.Value, opts)
		}
	}
}

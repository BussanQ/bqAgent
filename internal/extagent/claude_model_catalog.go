package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const claudeModelControlRequests = `{"type":"control_request","request_id":"models","request":{"subtype":"list_models"}}
{"type":"control_request","request_id":"settings","request":{"subtype":"get_settings"}}
{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}`

// These three replies share one metadata-only child and can arrive in any order.
// Older CLIs advertise their models in initialize, before list_models existed.
func readClaudeModelCatalog(ctx context.Context, control string, stdin io.WriteCloser, stdout io.ReadCloser, menu *ModelCatalog) error {
	var listed, initialized *ModelCatalog
	var listErr error
	var settings struct{ Model, Effort string }
	var gotSettings bool
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	err := readCatalogControl(ctx, AgentClaude, control, stdin, stdout, func(line []byte) (bool, error) {
		var frame struct {
			Type     string
			Response struct {
				ID             string `json:"request_id"`
				Subtype, Error string
				Response       json.RawMessage
			}
		}
		if json.Unmarshal(line, &frame) != nil || frame.Type != "control_response" {
			return false, nil
		}
		r := frame.Response
		switch r.ID {
		case "settings":
			gotSettings = true
			var result struct {
				Applied struct{ Model, Effort string }
			}
			if r.Subtype == "success" && json.Unmarshal(r.Response, &result) == nil {
				settings = result.Applied
			}
		case "models", "init":
			if r.Subtype == "success" {
				var result struct{ Models []claudeListedModel }
				if json.Unmarshal(r.Response, &result) != nil || result.Models == nil {
					if r.ID == "models" {
						return false, fmt.Errorf("Claude 返回的模型目录无效")
					}
					return false, nil
				}
				catalog := new(ModelCatalog)
				addClaudeModels(catalog, result.Models)
				if r.ID == "models" {
					listed = catalog
				} else {
					initialized = catalog
				}
			} else if r.ID == "models" {
				listErr = fmt.Errorf("Claude 模型目录：%s", r.Error)
				message := strings.ToLower(r.Error)
				unsupported := strings.Contains(message, "unknown request") || strings.Contains(message, "unknown control") ||
					strings.Contains(message, "unknown subtype") || strings.Contains(message, "unsupported request") ||
					strings.Contains(message, "unsupported control") || strings.Contains(message, "unsupported subtype") ||
					strings.Contains(message, "not supported") && strings.Contains(message, "list_models")
				if !unsupported {
					return false, listErr
				}
			}
		}
		usable := listed != nil || listErr != nil && initialized != nil
		if usable && !gotSettings && timer == nil {
			timer = time.AfterFunc(250*time.Millisecond, func() { _ = stdout.Close() })
		}
		return usable && gotSettings, nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && !errors.Is(err, io.EOF) && !(timer != nil && (errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe))) {
		return err
	}
	if listed == nil {
		listed = initialized
	}
	if listed == nil {
		if listErr != nil {
			return listErr
		}
		return fmt.Errorf("Claude 未返回模型目录")
	}
	*menu = *listed
	if settings.Model != "default" && validateModel(settings.Model) == nil {
		menu.DefaultModel = settings.Model
	}
	if menu.Efforts[menu.DefaultModel].supports(settings.Effort) {
		menu.SelectedEffort = settings.Effort
	}
	return nil
}

package extagent

import (
	"io"
	"slices"
	"strings"
	"testing"
)

func TestClaudePickerUsesLiveCatalogAndAppliedSettings(t *testing.T) {
	const models = `{"type":"control_response","response":{"request_id":"models","subtype":"success","response":{"models":[{"value":"default","resolvedModel":"first"},{"value":"first"},{"value":"other","supportsEffort":true,"supportedEffortLevels":["medium"]}]}}}`
	const init = `{"type":"control_response","response":{"request_id":"init","subtype":"success","response":{"models":[{"value":"legacy"},{"value":"disabled","disabled":true}]}}}`
	const settings = `{"type":"control_response","response":{"request_id":"settings","subtype":"success","response":{"applied":{"model":"other","effort":"medium"}}}}`
	const unsupported = `{"type":"control_response","response":{"request_id":"models","subtype":"error","error":"Unknown control request subtype: list_models"}}`
	const denied = `{"type":"control_response","response":{"request_id":"models","subtype":"error","error":"Unknown account or access denied"}}`
	const empty = `{"type":"control_response","response":{"request_id":"models","subtype":"success","response":{"models":[]}}}`
	for _, test := range []struct {
		name, frames, model, effort string
		rows                        []string
		fail                        bool
	}{
		{"list first", models + "\n" + settings, "other", "medium", []string{"first", "other"}, false},
		{"settings first", init + "\n" + settings + "\n" + models, "other", "medium", []string{"first", "other"}, false},
		{"settings absent", models, "first", "", []string{"first", "other"}, false},
		{"older CLI", unsupported + "\n" + init, "", "", []string{"legacy"}, false},
		{"older CLI missing list reply", init, "", "", []string{"legacy"}, false},
		{"live empty catalog", init + "\n" + empty, "", "", nil, false},
		{"access error", init + "\n" + denied, "", "", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var menu ModelCatalog
			var input catalogInput
			err := readCLIModels(t.Context(), AgentClaude, claudeModelControlRequests, &input, io.NopCloser(strings.NewReader(test.frames)), &menu)
			var ids []string
			for _, row := range menu.Models {
				ids = append(ids, row.ID)
			}
			if !input.closed || (err != nil) != test.fail || !slices.Equal(ids, test.rows) || menu.DefaultModel != test.model || menu.SelectedEffort != test.effort {
				t.Fatalf("menu=%+v error=%v closed=%v", menu, err, input.closed)
			}
		})
	}
}

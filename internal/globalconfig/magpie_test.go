package globalconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMagpieConnection(t *testing.T) {
	dir := t.TempDir()
	data := `{"model":"p/selected","base_url":"http://localhost:12345/v1/","api_key":"test-key","models":["p/other","p/selected","p/other"," "],"model_display_names":{"p/selected":"Selected","p/other":"Other"}}`
	if err := os.WriteFile(filepath.Join(dir, "magpie.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadMagpie(dir)
	if err != nil || config == nil {
		t.Fatalf("load: %v", err)
	}
	if config.APIKey != "test-key" || config.BaseURL != "http://localhost:12345/v1" || !reflect.DeepEqual(config.Models, []string{"p/selected", "p/other"}) || config.ModelDisplayNames.Name(config.Model) != "Selected" {
		t.Fatal("gateway or model catalog was not preserved")
	}
}

func TestLoadMagpieMissingDisconnectedAndInvalid(t *testing.T) {
	for _, test := range []struct {
		name, data string
		wantError  bool
	}{
		{name: "missing"},
		{name: "disconnected", data: `{}`},
		{name: "removed key", data: `{"model":"p/m","base_url":"http://localhost/v1"}`},
		{name: "removed model", data: `{"api_key":"test-key","base_url":"http://localhost/v1"}`},
		{name: "removed endpoint", data: `{"api_key":"test-key","model":"p/m"}`},
		{name: "bad JSON", data: `{`, wantError: true},
		{name: "bad URL", data: `{"model":"p/m","api_key":"test-key","base_url":"file:///tmp/key"}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.data != "" {
				if err := os.WriteFile(filepath.Join(dir, "magpie.json"), []byte(test.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			config, err := LoadMagpie(dir)
			if config != nil || (err != nil) != test.wantError {
				t.Fatalf("unexpected connection state, err=%v", err)
			}
		})
	}
}

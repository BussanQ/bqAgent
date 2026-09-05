package globalconfig

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSectionUpdatesPreserveOtherSections(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := store.UpdateWebUI(&WebUI{Password: "changed"})
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := store.UpdateProviders(func(active *string, providers *[]Provider) error {
				*active = "p"
				*providers = []Provider{{ID: "p", Name: "p", Models: []string{"m"}, DefaultModel: "m"}}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebUI.Password != "changed" || cfg.ActiveProvider != "p" {
		t.Fatalf("lost section: %+v", cfg)
	}
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	cfg, _ = store.Load()
	if cfg.WebUI.Password != "changed" {
		t.Fatal("overwritten")
	}
	info, _ := os.Stat(store.Path())
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if store != NewStore(filepath.Dir(store.Path())) {
		t.Fatal("store is not shared")
	}
}

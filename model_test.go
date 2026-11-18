package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local", "config.json")
	first, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	original := first.Get()
	if original.NodeID == "" || len(original.Secret) < 16 {
		t.Fatal("generated identifiers missing")
	}
	original.Name = "Главный OBS"
	original.Mode = "master"
	modeDefaults(&original)
	if err = first.Update(original); err != nil {
		t.Fatal(err)
	}
	second, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restored := second.Get()
	if restored.NodeID != original.NodeID || restored.Name != original.Name || restored.Secret != original.Secret {
		t.Fatal("persistent configuration changed")
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() < 100 {
		t.Fatal("invalid config file")
	}
}
func TestCameraPortsAreStable(t *testing.T) {
	cfg, err := newConfigStore(filepath.Join(t.TempDir(), "cfg.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfg.AssignCameraPort("cam-A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := cfg.AssignCameraPort("cam-B")
	if err != nil {
		t.Fatal(err)
	}
	again, err := cfg.AssignCameraPort("cam-A")
	if err != nil {
		t.Fatal(err)
	}
	if a != 5101 || b != 5102 || again != a {
		t.Fatalf("unexpected camera ports: %d %d %d", a, b, again)
	}
}
func TestRoleDefaults(t *testing.T) {
	c := defaultConfig()
	c.Mode = "camera-view"
	modeDefaults(&c)
	if !c.SendCamera || !c.WatchPresenter || c.SendScreen {
		t.Fatal("camera + viewer role invalid")
	}
	c.Mode = "presenter"
	modeDefaults(&c)
	if !c.SendScreen || c.WatchPresenter || c.SendCamera {
		t.Fatal("presenter role invalid")
	}
}

package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelRegistrySnapshotAndActivate(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldReg, oldActive := modelRegistryDir, modelRegistryPath, activeModelPath
	modelRegistryDir = filepath.Join(dir, "models")
	modelRegistryPath = filepath.Join(modelRegistryDir, "registry.json")
	activeModelPath = filepath.Join(dir, "model.json")
	t.Cleanup(func() {
		modelRegistryDir, modelRegistryPath, activeModelPath = oldDir, oldReg, oldActive
	})

	if err := os.WriteFile(activeModelPath, []byte("model-v1"), 0644); err != nil {
		t.Fatal(err)
	}

	v, err := RegisterCurrentModel(activeModelPath)
	if err != nil {
		t.Fatalf("RegisterCurrentModel: %v", err)
	}
	if v == nil || v.Name == "" {
		t.Fatalf("unexpected version: %+v", v)
	}

	// Registering the same hash must not duplicate entries.
	if _, err := RegisterCurrentModel(activeModelPath); err != nil {
		t.Fatal(err)
	}
	if got := len(ListModelVersions()); got != 1 {
		t.Fatalf("registry entries = %d, want 1", got)
	}

	// Simulate a newer active model, then roll back to the registered one.
	if err := os.WriteFile(activeModelPath, []byte("model-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ActivateModelVersion(v.File); err != nil {
		t.Fatalf("ActivateModelVersion: %v", err)
	}
	raw, _ := os.ReadFile(activeModelPath)
	if string(raw) != "model-v1" {
		t.Fatalf("active model = %q, want model-v1", string(raw))
	}

	if err := ActivateModelVersion(filepath.Join(dir, "outside.json")); err == nil {
		t.Fatal("activating a file outside the registry must fail")
	}
}

package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	modelRegistryDir = filepath.Join("data", "models")
	modelRegistryPath = filepath.Join(modelRegistryDir, "registry.json")
	activeModelPath   = "model.json"
)

type ModelVersion struct {
	File      string    `json:"file"`
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Active    bool      `json:"active"`
}

func hashFile(path string) (string, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw)), nil
}

func loadModelVersions() []ModelVersion {
	raw, err := os.ReadFile(modelRegistryPath)
	if err != nil {
		return nil
	}
	var versions []ModelVersion
	_ = json.Unmarshal(raw, &versions)
	return versions
}

func saveModelVersions(versions []ModelVersion) error {
	if err := os.MkdirAll(modelRegistryDir, 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return err
	}
	tmp := modelRegistryPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, modelRegistryPath)
}

// RegisterCurrentModel snapshots model.json into data/models once per hash.
func RegisterCurrentModel(modelPath string) (*ModelVersion, error) {
	hash, size, err := hashFile(modelPath)
	if err != nil {
		return nil, err
	}
	versions := loadModelVersions()
	for i := range versions {
		if versions[i].Hash == hash {
			return &versions[i], nil
		}
	}
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(modelRegistryDir, 0755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("model_%s_%s.json", time.Now().UTC().Format("20060102_150405"), hash[:8])
	dst := filepath.Join(modelRegistryDir, name)
	if err := os.WriteFile(dst, raw, 0644); err != nil {
		return nil, err
	}
	v := ModelVersion{File: dst, Name: filepath.Base(dst), Hash: hash, Size: size, CreatedAt: time.Now().UTC()}
	versions = append(versions, v)
	if err := saveModelVersions(versions); err != nil {
		return nil, err
	}
	return &v, nil
}

func ActiveModelHash() string {
	hash, _, err := hashFile(activeModelPath)
	if err != nil {
		return ""
	}
	return hash
}

// ListModelVersions returns registered models, newest first, flagging the active one.
func ListModelVersions() []ModelVersion {
	versions := loadModelVersions()
	active := ActiveModelHash()
	for i := range versions {
		if versions[i].Name == "" {
			versions[i].Name = filepath.Base(versions[i].File)
		}
		versions[i].Active = active != "" && versions[i].Hash == active
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].CreatedAt.After(versions[j].CreatedAt) })
	return versions
}

// ActivateModelVersion copies a registered model back to model.json.
func ActivateModelVersion(file string) error {
	absDir, err := filepath.Abs(modelRegistryDir)
	if err != nil {
		return err
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absDir, absFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("model file must live inside %s", modelRegistryDir)
	}
	raw, err := os.ReadFile(absFile)
	if err != nil {
		return err
	}
	return os.WriteFile(activeModelPath, raw, 0644)
}

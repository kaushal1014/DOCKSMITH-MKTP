package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Env        []string `json:"Env"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
}

type Layer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

type ImageManifest struct {
	Name    string  `json:"name"`
	Tag     string  `json:"tag"`
	Digest  string  `json:"digest"`
	Created string  `json:"created"`
	Config  Config  `json:"config"`
	Layers  []Layer `json:"layers"`
}

func saveManifest(state *State, m *ImageManifest) error {
    digest, err := computeManifestDigest(m)
    if err != nil {
        return err
    }
    m.Digest = digest 

    filename := m.Name + "_" + m.Tag + ".json"
    path := filepath.Join(state.Images, filename)

    data, err := json.MarshalIndent(m, "", "  ")
    if err != nil {
        return err
    }

    return os.WriteFile(path, data, 0644)
}

func loadManifest(state *State, nameTag string) (*ImageManifest, error) {
	filename := nameTag + ".json"
	path := filepath.Join(state.Images, filename)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var m ImageManifest
	err = json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func listImages(state *State) error {
	files, err := os.ReadDir(state.Images)
	if err != nil {
		return err
	}

	fmt.Printf("%-20s %-10s %-15s %-25s\n", "NAME", "TAG", "ID", "CREATED")

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		path := filepath.Join(state.Images, file.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var m ImageManifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		id := m.Digest
		if strings.HasPrefix(id, "sha256:") {
			id = id[len("sha256:"):]
		}
		if len(id) > 12 {
			id = id[:12]
		}

		fmt.Printf("%-20s %-10s %-15s %-25s\n",
			m.Name,
			m.Tag,
			id,
			m.Created,
		)
	}

	return nil
}

func removeImage(state *State, nameTag string) error {
	parts := strings.Split(nameTag, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid format, expected name:tag")
	}

	filename := parts[0] + "_" + parts[1] + ".json"
	path := filepath.Join(state.Images, filename)

	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return fmt.Errorf("image not found: %s", nameTag)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var m ImageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	for _, layer := range m.Layers {
		layerPath := filepath.Join(state.Layers, layer.Digest)
		os.Remove(layerPath)
	}

	return os.Remove(path)
}

func computeManifestDigest(m *ImageManifest) (string, error) {
	temp := *m
	temp.Digest = ""

	data, err := json.Marshal(temp)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}
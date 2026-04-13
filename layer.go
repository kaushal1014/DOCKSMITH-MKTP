package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func createCopyLayer(contextDir string, dest string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)

	var files []string

	// collect all files
	err = filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return Layer{}, err
	}

	// sort for determinism
	sort.Strings(files)

	for _, file := range files {
		rel, _ := filepath.Rel(contextDir, file)
		targetPath := filepath.Join(dest, rel)

		info, err := os.Stat(file)
		if err != nil {
			return Layer{}, err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return Layer{}, err
		}

		header.Name = targetPath
		header.ModTime = time.Unix(0, 0) // zero timestamp

		if err := tw.WriteHeader(header); err != nil {
			return Layer{}, err
		}

		f, err := os.Open(file)
		if err != nil {
			return Layer{}, err
		}

		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return Layer{}, err
		}
	}

	tw.Close()
	tempFile.Close()

	// compute hash
	data, err := os.ReadFile(tempFile.Name())
	if err != nil {
		return Layer{}, err
	}

	hash := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(hash[:])

	layerPath := filepath.Join(state.Layers, digest)

	err = os.WriteFile(layerPath, data, 0644)
	if err != nil {
		return Layer{}, err
	}

	return Layer{
		Digest:    digest,
		Size:      int64(len(data)),
		CreatedBy: "COPY " + contextDir + " " + dest,
	}, nil
}

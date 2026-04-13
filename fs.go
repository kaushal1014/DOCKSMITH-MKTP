package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func extractLayer(layerPath string, target string) error {
	file, err := os.Open(layerPath)
	if err != nil {
		return err
	}
	defer file.Close()

	tr := tar.NewReader(file)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		destPath := filepath.Join(target, header.Name)

		// ensure parent dirs exist
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}

		case tar.TypeReg:
			outFile, err := os.Create(destPath)
			if err != nil {
				return err
			}

			_, err = io.Copy(outFile, tr)
			outFile.Close()
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func extractAllLayers(layers []Layer, state *State, target string) error {
	for _, layer := range layers {
		layerPath := filepath.Join(state.Layers, layer.Digest)

		err := extractLayer(layerPath, target)
		if err != nil {
			return err
		}
	}
	return nil
}

func loadBaseImage(base string, state *State, target string) error {
	path := filepath.Join(state.Root, "base", base+".tar")

	// check exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("base image not found: %s", base)
	}

	return extractLayer(path, target)
}

func copyToRootFS(contextDir string, dest string, rootfs string) error {
	return filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}
		cleanDest := strings.TrimPrefix(dest, "/")
		targetPath := filepath.Join(rootfs, cleanDest, rel)


		if info.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		// ensure parent dir exists
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(targetPath)
		if err != nil {
			return err
		}
		_, err = io.Copy(dstFile, srcFile)
		dstFile.Close()
		return err
	})
}

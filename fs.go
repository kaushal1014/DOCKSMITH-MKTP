package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

		cleanName := strings.TrimPrefix(header.Name, "/")
		destPath := filepath.Join(target, cleanName)

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}

		case tar.TypeReg:
			outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			_, err = io.Copy(outFile, tr)
			outFile.Close()
			if err != nil {
				return err
			}
			if err := os.Chmod(destPath, os.FileMode(header.Mode)); err != nil {
				return err
			}

		case tar.TypeSymlink:
			linkTarget := header.Linkname
			os.Remove(destPath)
			if err := os.Symlink(linkTarget, destPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func extractAllLayers(layers []Layer, state *State, target string) error {
	for _, layer := range layers {
		layerPath := filepath.Join(state.Layers, layer.Digest)

		if _, err := os.Lstat(layerPath); os.IsNotExist(err) {
			return fmt.Errorf("layer file missing for digest %s — image may be broken (run 'docksmith rmi' and rebuild)", layer.Digest)
		}

		if err := extractLayer(layerPath, target); err != nil {
			return err
		}
	}
	return nil
}

func loadBaseImage(base string, state *State, target string) (string, error) {
	safeName := strings.ReplaceAll(base, ":", "_")
	path := filepath.Join(state.Root, "base", safeName+".tar")

	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("base image not found: %s (looked for %s) — import the base image before building", base, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading base image tar %s: %w", path, err)
	}

	hash := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(hash[:])

	layerPath := filepath.Join(state.Layers, digest)
	if _, err := os.Lstat(layerPath); os.IsNotExist(err) {
		// Not yet in the layer store — write it.
		if err := os.WriteFile(layerPath, data, 0644); err != nil {
			return "", fmt.Errorf("writing base layer to store: %w", err)
		}
	}

	// Extract into the build rootfs.
	if err := extractLayer(path, target); err != nil {
		return "", fmt.Errorf("extracting base image %s: %w", base, err)
	}

	return digest, nil
}

func resolveGlob(contextDir string, pattern string) ([]string, error) {
	if pattern == "." {
		return []string{contextDir}, nil
	}

	if !strings.ContainsAny(pattern, "*?[") {
		full := filepath.Join(contextDir, pattern)
		if _, err := os.Lstat(full); err != nil {
			return nil, fmt.Errorf("COPY src not found: %s", pattern)
		}
		return []string{full}, nil
	}

	if strings.Contains(pattern, "**") {
		var matches []string
		err := filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(contextDir, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			matched, err := matchDoubleGlob(pattern, rel)
			if err != nil {
				return err
			}
			if matched {
				matches = append(matches, path)
			}
			return nil
		})
		sort.Strings(matches)
		return matches, err
	}

	globPattern := filepath.Join(contextDir, pattern)
	matches, err := filepath.Glob(globPattern)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("COPY src matched no files: %s", pattern)
	}
	sort.Strings(matches)
	return matches, nil
}

func matchDoubleGlob(pattern, rel string) (bool, error) {
	patParts := strings.Split(filepath.ToSlash(pattern), "/")
	relParts := strings.Split(filepath.ToSlash(rel), "/")
	return matchSegments(patParts, relParts)
}

func matchSegments(pat, rel []string) (bool, error) {
	if len(pat) == 0 && len(rel) == 0 {
		return true, nil
	}
	if len(pat) == 0 {
		return false, nil
	}
	if pat[0] == "**" {
		for i := 0; i <= len(rel); i++ {
			ok, err := matchSegments(pat[1:], rel[i:])
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if len(rel) == 0 {
		return false, nil
	}
	matched, err := filepath.Match(pat[0], rel[0])
	if err != nil || !matched {
		return false, err
	}
	return matchSegments(pat[1:], rel[1:])
}


func copyGlobToRootFS(srcs []string, contextDir string, dest string, rootfs string) error {
	cleanDest := strings.TrimPrefix(dest, "/")
	for _, src := range srcs {
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(contextDir, path)
				if err != nil {
					return err
				}
				targetPath := filepath.Join(rootfs, cleanDest, rel)
				if fi.IsDir() {
					return os.MkdirAll(targetPath, 0755)
				}
				if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
					return err
				}
				return copyFile(path, targetPath)
			})
			if err != nil {
				return err
			}
		} else {
			rel, err := filepath.Rel(contextDir, src)
			if err != nil {
				rel = filepath.Base(src)
			}
			targetPath := filepath.Join(rootfs, cleanDest, rel)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			if err := copyFile(src, targetPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(dstFile, srcFile)
	dstFile.Close()
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode().Perm())
}
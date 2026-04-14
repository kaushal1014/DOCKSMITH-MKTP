package main

import (
	"archive/tar"
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
			outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			_, err = io.Copy(outFile, tr)
			outFile.Close()
			if err != nil {
				return err
			}
			// DEBUG
			fmt.Printf("  [extract] %s mode=%o\n", header.Name, header.Mode)
			if err := os.Chmod(destPath, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			linkTarget := header.Linkname

			// remove existing file if any
			os.Remove(destPath)

			err := os.Symlink(linkTarget, destPath)
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
	// Sanitize the base image name for use as a filename (replace ':' with '_').
	safeName := strings.ReplaceAll(base, ":", "_")
	path := filepath.Join(state.Root, "base", safeName+".tar")

	// check exists
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return fmt.Errorf("base image not found: %s (looked for %s)", base, path)
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

// resolveGlob expands a src pattern (relative to contextDir) into a sorted
// list of matching absolute paths. Supports * and ** globs.
// Special case: "." returns []string{contextDir}.
func resolveGlob(contextDir string, pattern string) ([]string, error) {
	if pattern == "." {
		return []string{contextDir}, nil
	}

	// If no glob chars, treat as literal path.
	if !strings.ContainsAny(pattern, "*?[") {
		full := filepath.Join(contextDir, pattern)
		if _, err := os.Lstat(full); err != nil {
			return nil, fmt.Errorf("COPY src not found: %s", pattern)
		}
		return []string{full}, nil
	}

	// ** glob: walk entire tree and match each relative path.
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

	// Regular glob (supports *  and ?).
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

// matchDoubleGlob matches a relative path against a pattern that may contain **.
// ** matches any number of path segments (including zero).
func matchDoubleGlob(pattern, rel string) (bool, error) {
	// Split both into segments.
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
		// ** can consume zero or more segments.
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

// copyGlobToRootFS copies all files matched by srcs into dest inside rootfs.
// Each src is an absolute path; if it is a directory its contents are walked.
func copyGlobToRootFS(srcs []string, contextDir string, dest string, rootfs string) error {
	cleanDest := strings.TrimPrefix(dest, "/")
	for _, src := range srcs {
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Walk the directory, preserving relative structure under dest.
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
			// Single file: place directly under dest.
			rel, err := filepath.Rel(contextDir, src)
			if err != nil {
				// src is not under contextDir — use basename.
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
	// Use source file's permission bits so execute bits are preserved.
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(dstFile, srcFile)
	dstFile.Close()
	if err != nil {
		return err
	}
	// Explicit chmod overrides any umask applied by OpenFile.
	return os.Chmod(dst, srcInfo.Mode().Perm())
}
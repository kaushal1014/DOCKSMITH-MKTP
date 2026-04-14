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
	"time"
)


func createCopyLayer(contextDir string, dest string, createdBy string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)

	var files []string

	// collect all files
	err = filepath.Walk(contextDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return Layer{}, err
	}

	sort.Strings(files)

	cleanDest := strings.TrimPrefix(dest, "/")

	for _, p := range files {
		info, err := os.Lstat(p)
		if err != nil {
			return Layer{}, err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return Layer{}, err
		}

		// Use path relative to contextDir, prefixed with dest.
		// e.g. contextDir=/home/k/Docksmith, p=/home/k/Docksmith/hello.sh, dest=/app
		// → header.Name = app/hello.sh
		rel, err := filepath.Rel(contextDir, p)
		if err != nil {
			return Layer{}, err
		}
		header.Name = filepath.Join(cleanDest, rel)
		header.ModTime = time.Unix(0, 0)
		header.Size = info.Size()

		if err := tw.WriteHeader(header); err != nil {
			return Layer{}, err
		}

		file, err := os.Open(p)
		if err != nil {
			return Layer{}, err
		}

		_, err = io.Copy(tw, file)
		file.Close()
		if err != nil {
			return Layer{}, err
		}
	}

	tw.Close()
	tempFile.Close()

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
		CreatedBy: createdBy,
	}, nil
}

func createLayerFromChanges(rootfs string, files []string, createdBy string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)

	// Sort for reproducibility.
	sorted := make([]string, len(files))
	copy(sorted, files)
	sort.Strings(sorted)

	for _, relPath := range sorted {
		fullPath := filepath.Join(rootfs, relPath)

		linfo, err := os.Lstat(fullPath)
		if err != nil {
			return Layer{}, err
		}

		if linfo.IsDir() {
			continue
		}

		if linfo.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(fullPath)
			if err != nil {
				return Layer{}, err
			}
			header := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     relPath,
				Linkname: target,
				ModTime:  time.Unix(0, 0),
				Mode:     int64(linfo.Mode()),
			}
			if err := tw.WriteHeader(header); err != nil {
				return Layer{}, err
			}
			continue
		}

		header, err := tar.FileInfoHeader(linfo, "")
		if err != nil {
			return Layer{}, err
		}

		header.Name = relPath
		header.ModTime = time.Unix(0, 0)
		header.Size = linfo.Size()

		// DEBUG: print mode being stored in layer
		fmt.Printf("  [layer] %s mode=%o\n", relPath, linfo.Mode().Perm())

		if err := tw.WriteHeader(header); err != nil {
			return Layer{}, err
		}

		file, err := os.Open(fullPath)
		if err != nil {
			return Layer{}, err
		}

		_, err = io.Copy(tw, file)
		file.Close()
		if err != nil {
			return Layer{}, err
		}
	}

	tw.Close()
	tempFile.Close()

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
		CreatedBy: createdBy,
	}, nil
}

// createCopyLayerFromSrcs builds a tar layer from a list of resolved absolute
// source paths. Each file is stored in the tar relative to contextDir, prefixed
// with dest — matching exactly what copyGlobToRootFS writes to the rootfs.
func createCopyLayerFromSrcs(srcs []string, contextDir string, dest string, createdBy string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)
	cleanDest := strings.TrimPrefix(dest, "/")

	// Collect all individual files from srcs (expanding dirs).
	type entry struct {
		fullPath string
		tarName  string
	}
	var entries []entry

	for _, src := range srcs {
		info, err := os.Lstat(src)
		if err != nil {
			return Layer{}, err
		}
		if info.IsDir() {
			err = filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if fi.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(contextDir, path)
				if err != nil {
					return err
				}
				entries = append(entries, entry{path, filepath.Join(cleanDest, rel)})
				return nil
			})
			if err != nil {
				return Layer{}, err
			}
		} else {
			rel, err := filepath.Rel(contextDir, src)
			if err != nil {
				rel = filepath.Base(src)
			}
			entries = append(entries, entry{src, filepath.Join(cleanDest, rel)})
		}
	}

	// Sort by tar name for reproducibility.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].tarName < entries[j].tarName
	})

	for _, e := range entries {
		info, err := os.Lstat(e.fullPath)
		if err != nil {
			return Layer{}, err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return Layer{}, err
		}
		header.Name = e.tarName
		header.ModTime = time.Unix(0, 0)
		header.Size = info.Size()

		if err := tw.WriteHeader(header); err != nil {
			return Layer{}, err
		}
		f, err := os.Open(e.fullPath)
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

	data, err := os.ReadFile(tempFile.Name())
	if err != nil {
		return Layer{}, err
	}

	hash := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	layerPath := filepath.Join(state.Layers, digest)

	if err := os.WriteFile(layerPath, data, 0644); err != nil {
		return Layer{}, err
	}

	return Layer{
		Digest:    digest,
		Size:      int64(len(data)),
		CreatedBy: createdBy,
	}, nil
}
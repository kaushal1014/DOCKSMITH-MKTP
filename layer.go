package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func createLayerFromChanges(rootfs string, files []string, createdBy string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)

	sorted := make([]string, len(files))
	copy(sorted, files)
	sort.Strings(sorted)

	for _, relPath := range sorted {
		fullPath := filepath.Join(rootfs, relPath)

		linfo, err := os.Lstat(fullPath)
		if err != nil {
			return Layer{}, err
		}

		if linfo.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(fullPath)
			if err != nil {
				return Layer{}, err
			}
			header := &tar.Header{
				Typeflag:   tar.TypeSymlink,
				Name:       relPath,
				Linkname:   target,
				ModTime:    time.Unix(0, 0),
				AccessTime: time.Time{},
				ChangeTime: time.Time{},
				Format:     tar.FormatPAX,
				Mode:       0777,
			}
			if err := tw.WriteHeader(header); err != nil {
				return Layer{}, err
			}
			continue
		}

		if linfo.IsDir() {
			header := &tar.Header{
				Typeflag:   tar.TypeDir,
				Name:       relPath + "/",
				ModTime:    time.Unix(0, 0),
				AccessTime: time.Time{},
				ChangeTime: time.Time{},
				Format:     tar.FormatPAX,
				Mode:       int64(linfo.Mode() & 0777),
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
		header.Format = tar.FormatPAX
		header.Mode = int64(linfo.Mode() & 0777)
		header.Size = linfo.Size()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""

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

func createCopyLayerFromSrcs(srcs []string, contextDir string, dest string, createdBy string, state *State) (Layer, error) {
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return Layer{}, err
	}
	defer os.Remove(tempFile.Name())

	tw := tar.NewWriter(tempFile)
	cleanDest := strings.TrimPrefix(dest, "/")

	type entry struct {
		fullPath string
		tarName  string
		isDir    bool
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
				rel, err := filepath.Rel(contextDir, path)
				if err != nil {
					return err
				}
				entries = append(entries, entry{path, filepath.Join(cleanDest, rel), fi.IsDir()})
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
			entries = append(entries, entry{src, filepath.Join(cleanDest, rel), false})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].tarName < entries[j].tarName
	})

	for _, e := range entries {
		info, err := os.Lstat(e.fullPath)
		if err != nil {
			return Layer{}, err
		}

		if e.isDir {
			header := &tar.Header{
				Typeflag:   tar.TypeDir,
				Name:       e.tarName + "/",
				ModTime:    time.Unix(0, 0),
				AccessTime: time.Time{},
				ChangeTime: time.Time{},
				Format:     tar.FormatPAX,
				Mode:       int64(info.Mode() & 0777),
				Uid:        0,
				Gid:        0,
			}
			if err := tw.WriteHeader(header); err != nil {
				return Layer{}, err
			}
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return Layer{}, err
		}
		header.Name = e.tarName
		header.ModTime = time.Unix(0, 0)
		header.Format = tar.FormatPAX
		header.Mode = int64(info.Mode() & 0777)
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Size = info.Size()
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""

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
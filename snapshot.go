package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func snapshotFS(root string) (map[string]string, error) {
	snapshot := make(map[string]string)

	// Use "sudo find" to enumerate files so that root-owned files/dirs created
	// by a previous "sudo chroot" RUN step are fully visible. Without this,
	// os.Open fails on root-owned files and the mode change goes undetected,
	// producing an empty diff and losing the chmod layer entirely.
	out, err := exec.Command("sudo", "find", root, "-not", "-type", "d", "-print0").Output()
	if err != nil {
		// Fall back to unprivileged walk if sudo find fails.
		return snapshotFSUnprivileged(root)
	}

	paths := strings.Split(string(out), "\x00")
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}

		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			continue
		}

		linfo, err := os.Lstat(path)
		if err != nil {
			// File may be root-owned and we can't stat it — skip.
			continue
		}

		if linfo.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				continue
			}
			snapshot[rel] = "symlink:" + target
			continue
		}

		// Hash via sudo cat so root-owned unreadable files are still hashed.
		hash, err := hashFileSudo(path)
		if err != nil {
			// Last resort: record mode only so a chmod is still detected.
			snapshot[rel] = fmt.Sprintf("nohash:%o", linfo.Mode().Perm())
			continue
		}
		snapshot[rel] = fmt.Sprintf("%s:%o", hash, linfo.Mode().Perm())
	}

	return snapshot, nil
}

// snapshotFSUnprivileged is the original walk-based snapshot for contexts where
// sudo is unavailable (e.g. unit tests).
func snapshotFSUnprivileged(root string) (map[string]string, error) {
	snapshot := make(map[string]string)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		linfo, err := os.Lstat(path)
		if err != nil {
			// Propagate error instead of silently skipping (old bug).
			return err
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[rel] = "symlink:" + target
			return nil
		}

		hash, err := hashFile(path)
		if err != nil {
			// Propagate error instead of silently skipping (old bug).
			return err
		}
		snapshot[rel] = fmt.Sprintf("%s:%o", hash, linfo.Mode().Perm())
		return nil
	})

	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// hashFileSudo reads a file via "sudo cat" so root-owned files are accessible.
func hashFileSudo(path string) (string, error) {
	cmd := exec.Command("sudo", "cat", path)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(out)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := sha256.New()
	_, err = io.Copy(h, file)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func diffSnapshots(before, after map[string]string) []string {
	var changed []string

	for path, newHash := range after {
		oldHash, exists := before[path]

		// new file OR modified file (content or mode)
		if !exists || oldHash != newHash {
			changed = append(changed, path)
		}
	}

	return changed
}
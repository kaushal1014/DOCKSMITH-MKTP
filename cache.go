package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cacheIndex is the on-disk map: cacheKey -> layerDigest
type cacheIndex map[string]string

func loadCacheIndex(state *State) (cacheIndex, error) {
	path := filepath.Join(state.Cache, "index.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(cacheIndex), nil
	}
	if err != nil {
		return nil, err
	}
	var idx cacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return idx, nil
}

func saveCacheIndex(state *State, idx cacheIndex) error {
	path := filepath.Join(state.Cache, "index.json")
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// lookupCache returns (layerDigest, hit).
// A hit requires the key to exist AND the layer file to be present on disk.
func lookupCache(state *State, idx cacheIndex, key string) (string, bool) {
	digest, ok := idx[key]
	if !ok {
		return "", false
	}
	layerPath := filepath.Join(state.Layers, digest)
	if _, err := os.Lstat(layerPath); err != nil {
		return "", false
	}
	return digest, true
}

func storeCache(state *State, idx cacheIndex, key string, digest string) error {
	idx[key] = digest
	return saveCacheIndex(state, idx)
}

// computeCacheKey builds the deterministic cache key for a COPY or RUN instruction.
//
// Key inputs (all hashed together):
//   - prevDigest    : digest of the previous layer (or base image manifest digest)
//   - instrText     : full raw instruction line from the Docksmithfile
//   - workdir       : current WORKDIR value (empty string if not set)
//   - envState      : all KEY=VALUE pairs accumulated so far, sorted by key
//   - srcHashes     : (COPY only) SHA-256 of each source file, sorted by path
func computeCacheKey(prevDigest string, instrText string, workdir string, envState []string, srcHashes map[string]string) string {
	h := sha256.New()

	h.Write([]byte(prevDigest))
	h.Write([]byte("\x00"))
	h.Write([]byte(instrText))
	h.Write([]byte("\x00"))
	h.Write([]byte(workdir))
	h.Write([]byte("\x00"))

	// ENV state: sort by key for determinism
	sortedEnv := make([]string, len(envState))
	copy(sortedEnv, envState)
	sort.Slice(sortedEnv, func(i, j int) bool {
		ki := strings.SplitN(sortedEnv[i], "=", 2)[0]
		kj := strings.SplitN(sortedEnv[j], "=", 2)[0]
		return ki < kj
	})
	h.Write([]byte(strings.Join(sortedEnv, "\n")))
	h.Write([]byte("\x00"))

	// COPY source file hashes: sort by path for determinism
	if len(srcHashes) > 0 {
		paths := make([]string, 0, len(srcHashes))
		for p := range srcHashes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			h.Write([]byte(p))
			h.Write([]byte(":"))
			h.Write([]byte(srcHashes[p]))
			h.Write([]byte("\n"))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

// hashContextFiles returns a map of relPath -> sha256 hex for every file under dir.
func hashContextFiles(dir string) (map[string]string, error) {
	result := make(map[string]string)
	err := walkSorted(dir, func(relPath string, fullPath string) error {
		h, err := hashFile(fullPath)
		if err != nil {
			return err
		}
		result[relPath] = h
		return nil
	})
	return result, err
}

// walkSorted walks dir and calls fn for each file in sorted order.
func walkSorted(dir string, fn func(relPath string, fullPath string) error) error {
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if err := fn(rel, p); err != nil {
			return err
		}
	}
	return nil
}
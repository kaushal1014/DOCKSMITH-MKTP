package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BuildState struct {
	BaseImage          string
	BaseManifestDigest string
	RootFS             string

	Env        []string
	Cmd        []string
	WorkingDir string

	Layers     []Layer
	BaseLayers []Layer // layers inherited from the base image
}

type BuildOptions struct {
	NoCache bool
	Name    string
	Tag     string
}

func executeInstructions(instructions []Instruction, context string, state *State, opts BuildOptions) (*BuildState, string, error) {
	buildState := &BuildState{}

	cacheIdx, err := loadCacheIndex(state)
	if err != nil {
		return nil, "", fmt.Errorf("loading cache index: %w", err)
	}

	prevDigest := ""
	cacheBroken := false
	allCacheHits := true

	totalSteps := len(instructions)
	stepNum := 0
	totalStart := time.Now()

	// Harvest existing created timestamp for cache-hit rebuilds.
	originalCreated := ""
	if existing, _ := loadManifest(state, opts.Name+"_"+opts.Tag); existing != nil {
		originalCreated = existing.Created
	}

	for _, inst := range instructions {
		stepNum++

		switch inst.Type {

		// ------------------------------------------------------------------ FROM
		case "FROM":
			if len(inst.Args) != 1 {
				return nil, "", fmt.Errorf("FROM requires exactly 1 argument")
			}
			base := inst.Args[0]
			fmt.Printf("Step %d/%d : FROM %s\n", stepNum, totalSteps, base)

			buildState.BaseImage = base

			rootfs, err := os.MkdirTemp("", "docksmith-build-*")
			if err != nil {
				return nil, "", err
			}
			buildState.RootFS = rootfs

			if err := loadBaseImage(base, state, rootfs); err != nil {
				return nil, "", err
			}

			// Try to load a Docksmith manifest for the base image so we can
			// include its layers and use its digest as the first prevDigest.
			if baseManifest, err := loadBaseManifestByName(base, state); err == nil {
				buildState.BaseLayers = baseManifest.Layers
				prevDigest = baseManifest.Digest
			} else {
				// Plain imported tarball — derive digest from the tar bytes.
				prevDigest = digestBaseImageTar(base, state)
			}
			buildState.BaseManifestDigest = prevDigest

		// ---------------------------------------------------------------- WORKDIR
		case "WORKDIR":
			if len(inst.Args) != 1 {
				return nil, "", fmt.Errorf("WORKDIR requires exactly 1 argument")
			}
			buildState.WorkingDir = inst.Args[0]
			fmt.Printf("Step %d/%d : WORKDIR %s\n", stepNum, totalSteps, inst.Args[0])

		// ------------------------------------------------------------------- ENV
		case "ENV":
			if len(inst.Args) != 1 {
				return nil, "", fmt.Errorf("ENV requires KEY=VALUE")
			}
			buildState.Env = append(buildState.Env, inst.Args[0])
			fmt.Printf("Step %d/%d : ENV %s\n", stepNum, totalSteps, inst.Args[0])

		// ------------------------------------------------------------------- CMD
		case "CMD":
			cmd, err := parseCMD(inst.Args)
			if err != nil {
				return nil, "", err
			}
			buildState.Cmd = cmd
			fmt.Printf("Step %d/%d : CMD %s\n", stepNum, totalSteps, inst.Args[0])

		// ------------------------------------------------------------------ COPY
		case "COPY":
			if len(inst.Args) != 2 {
				return nil, "", fmt.Errorf("COPY requires src and dest")
			}
			src := inst.Args[0]
			dest := inst.Args[1]

			// Ensure WORKDIR exists before copying into the rootfs.
			if buildState.WorkingDir != "" {
				if err := os.MkdirAll(filepath.Join(buildState.RootFS, buildState.WorkingDir), 0755); err != nil {
					return nil, "", err
				}
			}

			// Resolve glob pattern to a list of absolute source paths.
			srcs, err := resolveGlob(context, src)
			if err != nil {
				return nil, "", err
			}

			// Compute source file hashes for the cache key (COPY-specific).
			// Hash all files across all matched srcs, keyed by path relative to context.
			srcHashes := make(map[string]string)
			for _, s := range srcs {
				h, err := hashContextFiles(s)
				if err != nil {
					return nil, "", err
				}
				for k, v := range h {
					rel, _ := filepath.Rel(context, filepath.Join(s, k))
					srcHashes[rel] = v
				}
			}

			cacheKey := computeCacheKey(prevDigest, inst.Raw, buildState.WorkingDir, buildState.Env, srcHashes)
			stepStart := time.Now()

			if !opts.NoCache && !cacheBroken {
				if digest, hit := lookupCache(state, cacheIdx, cacheKey); hit {
					elapsed := time.Since(stepStart)
					fmt.Printf("Step %d/%d : COPY %s %s [CACHE HIT] %.2fs\n", stepNum, totalSteps, src, dest, elapsed.Seconds())

					layerPath := filepath.Join(state.Layers, digest)
					info, _ := os.Stat(layerPath)
					layer := Layer{Digest: digest, Size: info.Size(), CreatedBy: inst.Raw}

					if err := extractLayer(layerPath, buildState.RootFS); err != nil {
						return nil, "", err
					}
					buildState.Layers = append(buildState.Layers, layer)
					prevDigest = digest
					continue
				}
			}

			// Cache miss.
			cacheBroken = true
			allCacheHits = false

			if err := copyGlobToRootFS(srcs, context, dest, buildState.RootFS); err != nil {
				return nil, "", err
			}
			layer, err := createCopyLayerFromSrcs(srcs, context, dest, inst.Raw, state)
			if err != nil {
				return nil, "", err
			}

			elapsed := time.Since(stepStart)
			fmt.Printf("Step %d/%d : COPY %s %s [CACHE MISS] %.2fs\n", stepNum, totalSteps, src, dest, elapsed.Seconds())

			if !opts.NoCache {
				if err := storeCache(state, cacheIdx, cacheKey, layer.Digest); err != nil {
					return nil, "", err
				}
			}
			buildState.Layers = append(buildState.Layers, layer)
			prevDigest = layer.Digest

		// ------------------------------------------------------------------- RUN
		case "RUN":
			cmdStr := strings.Join(inst.Args, " ")

			// Ensure WORKDIR exists before running.
			if buildState.WorkingDir != "" {
				if err := os.MkdirAll(filepath.Join(buildState.RootFS, buildState.WorkingDir), 0755); err != nil {
					return nil, "", err
				}
			}

			cacheKey := computeCacheKey(prevDigest, inst.Raw, buildState.WorkingDir, buildState.Env, nil)
			stepStart := time.Now()

			if !opts.NoCache && !cacheBroken {
				if digest, hit := lookupCache(state, cacheIdx, cacheKey); hit {
					elapsed := time.Since(stepStart)
					fmt.Printf("Step %d/%d : RUN %s [CACHE HIT] %.2fs\n", stepNum, totalSteps, cmdStr, elapsed.Seconds())

					layerPath := filepath.Join(state.Layers, digest)
					info, _ := os.Stat(layerPath)
					layer := Layer{Digest: digest, Size: info.Size(), CreatedBy: inst.Raw}

					if err := extractLayer(layerPath, buildState.RootFS); err != nil {
						return nil, "", err
					}
					buildState.Layers = append(buildState.Layers, layer)
					prevDigest = digest
					continue
				}
			}

			// Cache miss — execute inside the rootfs.
			cacheBroken = true
			allCacheHits = false

			before, err := snapshotFS(buildState.RootFS)
			if err != nil {
				return nil, "", err
			}

			runCmd := cmdStr
			if buildState.WorkingDir != "" {
				runCmd = "cd " + buildState.WorkingDir + " && " + cmdStr
			}

			fmt.Printf("Step %d/%d : RUN %s\n", stepNum, totalSteps, cmdStr)
			if err := runCommandChroot(buildState.RootFS, []string{runCmd}, buildState.Env); err != nil {
				return nil, "", err
			}

			after, err := snapshotFS(buildState.RootFS)
			if err != nil {
				return nil, "", err
			}
			changes := diffSnapshots(before, after)

			layer, err := createLayerFromChanges(buildState.RootFS, changes, inst.Raw, state)
			if err != nil {
				return nil, "", err
			}

			elapsed := time.Since(stepStart)
			fmt.Printf(" ---> [CACHE MISS] %.2fs\n", elapsed.Seconds())

			if !opts.NoCache {
				if err := storeCache(state, cacheIdx, cacheKey, layer.Digest); err != nil {
					return nil, "", err
				}
			}
			buildState.Layers = append(buildState.Layers, layer)
			prevDigest = layer.Digest
		}
	}

	// Determine created timestamp: preserve original if all steps were cache hits.
	created := time.Now().Format(time.RFC3339)
	if allCacheHits && originalCreated != "" {
		created = originalCreated
	}

	totalElapsed := time.Since(totalStart)
	_ = totalElapsed

	return buildState, created, nil
}

// loadBaseManifestByName loads the Docksmith manifest for a base image.
func loadBaseManifestByName(base string, state *State) (*ImageManifest, error) {
	nameTag := strings.ReplaceAll(base, ":", "_")
	if !strings.Contains(base, ":") {
		nameTag = base + "_latest"
	}
	return loadManifest(state, nameTag)
}

// digestBaseImageTar returns a sha256 hex digest of the raw base tarball.
// Used when the base image has no Docksmith manifest (plain imported tar).
func digestBaseImageTar(base string, state *State) string {
	safeName := strings.ReplaceAll(base, ":", "_")
	path := filepath.Join(state.Root, "base", safeName+".tar")
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown:" + base
	}
	return hashBytes(data)
}

// hashBytes returns "sha256:<hex>" for the given data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseCMD(args []string) ([]string, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("CMD must be in JSON array form")
	}
	var cmd []string
	if err := json.Unmarshal([]byte(args[0]), &cmd); err != nil {
		return nil, fmt.Errorf("invalid CMD format, must be JSON array")
	}
	return cmd, nil
}
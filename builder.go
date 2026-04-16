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
	BaseLayers []Layer 
}

type BuildOptions struct {
	NoCache bool
	Name    string
	Tag     string
}

func buildEnv(imageEnv []string) []string {
	defaults := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm",
	}
	result := append([]string{}, defaults...)
	for _, kv := range imageEnv {
		key := strings.SplitN(kv, "=", 2)[0]
		filtered := result[:0]
		for _, existing := range result {
			if !strings.HasPrefix(existing, key+"=") {
				filtered = append(filtered, existing)
			}
		}
		result = append(filtered, kv)
	}
	return result
}

func executeInstructions(instructions []Instruction, context string, state *State, opts BuildOptions) (*BuildState, string, error) {
	buildState := &BuildState{}

	cacheIdx, err := loadCacheIndex(state)
	if err != nil {
		return nil, "", fmt.Errorf("loading cache index: %w", err)
	}

	prevDigest := ""
	cacheBroken := false

	totalSteps := len(instructions)
	stepNum := 0
	totalStart := time.Now()

	originalCreated := ""
	if existing, _ := loadManifest(state, opts.Name+"_"+opts.Tag); existing != nil {
		originalCreated = existing.Created
	}

	for _, inst := range instructions {
		stepNum++

		switch inst.Type {
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

			baseLayerDigest, err := loadBaseImage(base, state, rootfs)
			if err != nil {
				return nil, "", err
			}

			if baseManifest, err := loadBaseManifestByName(base, state); err == nil {
				buildState.BaseLayers = baseManifest.Layers
				prevDigest = baseManifest.Digest
			} else {
				info, _ := os.Stat(filepath.Join(state.Layers, baseLayerDigest))
				var size int64
				if info != nil {
					size = info.Size()
				}
				buildState.BaseLayers = []Layer{
					{Digest: baseLayerDigest, Size: size, CreatedBy: "FROM " + base},
				}
				prevDigest = baseLayerDigest
			}
			buildState.BaseManifestDigest = prevDigest

		case "WORKDIR":
			if len(inst.Args) != 1 {
				return nil, "", fmt.Errorf("WORKDIR requires exactly 1 argument")
			}
			buildState.WorkingDir = inst.Args[0]
			fmt.Printf("Step %d/%d : WORKDIR %s\n", stepNum, totalSteps, inst.Args[0])

		case "ENV":
			if len(inst.Args) != 1 {
				return nil, "", fmt.Errorf("ENV requires KEY=VALUE")
			}
			buildState.Env = append(buildState.Env, inst.Args[0])
			fmt.Printf("Step %d/%d : ENV %s\n", stepNum, totalSteps, inst.Args[0])

		case "CMD":
			cmd, err := parseCMD(inst.Args)
			if err != nil {
				return nil, "", err
			}
			buildState.Cmd = cmd
			fmt.Printf("Step %d/%d : CMD %s\n", stepNum, totalSteps, inst.Args[0])

		case "COPY":
			if len(inst.Args) != 2 {
				return nil, "", fmt.Errorf("COPY requires src and dest")
			}
			src := inst.Args[0]
			dest := inst.Args[1]

			if buildState.WorkingDir != "" {
				if err := os.MkdirAll(filepath.Join(buildState.RootFS, buildState.WorkingDir), 0755); err != nil {
					return nil, "", err
				}
			}

			srcs, err := resolveGlob(context, src)
			if err != nil {
				return nil, "", err
			}

			srcHashes := make(map[string]string)
			for _, s := range srcs {
				info, err := os.Lstat(s)
				if err != nil {
					return nil, "", err
				}
				if info.IsDir() {
					h, err := hashContextFiles(s)
					if err != nil {
						return nil, "", err
					}
					for relInDir, digest := range h {
						absPath := filepath.Join(s, relInDir)
						relToCtx, err := filepath.Rel(context, absPath)
						if err != nil {
							return nil, "", err
						}
						srcHashes[relToCtx] = digest
					}
				} else {
					relToCtx, err := filepath.Rel(context, s)
					if err != nil {
						return nil, "", err
					}
					h, err := hashFile(s)
					if err != nil {
						return nil, "", err
					}
					srcHashes[relToCtx] = h
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

			cacheBroken = true

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

		case "RUN":
			cmdStr := strings.Join(inst.Args, " ")

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

			cacheBroken = true

			before, err := snapshotFS(buildState.RootFS)
			if err != nil {
				return nil, "", err
			}

			runCmd := cmdStr
			if buildState.WorkingDir != "" {
				runCmd = "cd " + buildState.WorkingDir + " && " + cmdStr
			}

			runEnv := buildEnv(buildState.Env)

			if err := runCommandChroot(buildState.RootFS, []string{runCmd}, runEnv); err != nil {
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
			fmt.Printf("Step %d/%d : RUN %s [CACHE MISS] %.2fs\n", stepNum, totalSteps, cmdStr, elapsed.Seconds())

			if !opts.NoCache {
				if err := storeCache(state, cacheIdx, cacheKey, layer.Digest); err != nil {
					return nil, "", err
				}
			}
			buildState.Layers = append(buildState.Layers, layer)
			prevDigest = layer.Digest
		}
	}

	created := originalCreated
	if created == "" {
		created = time.Unix(0, 0).Format(time.RFC3339)
	}

	totalElapsed := time.Since(totalStart)
	_ = totalElapsed

	return buildState, created, nil
}

func loadBaseManifestByName(base string, state *State) (*ImageManifest, error) {
	nameTag := strings.ReplaceAll(base, ":", "_")
	if !strings.Contains(base, ":") {
		nameTag = base + "_latest"
	}
	return loadManifest(state, nameTag)
}

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
		return nil, fmt.Errorf("invalid CMD format, must be JSON array: %w", err)
	}
	return cmd, nil
}
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type EnvList []string

func (e *EnvList) String() string { return "" }
func (e *EnvList) Set(value string) error {
	*e = append(*e, value)
	return nil
}

func main() {
	state, err := initState()
	if err != nil {
		fmt.Println("Error initializing state:", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("expected 'build', 'run', 'images', or 'rmi'")
		os.Exit(1)
	}

	switch os.Args[1] {

	// ------------------------------------------------------------------ build
	case "build":
		buildCmd := flag.NewFlagSet("build", flag.ExitOnError)
		tag := buildCmd.String("t", "", "image tag (name:tag)")
		noCache := buildCmd.Bool("no-cache", false, "disable cache")
		buildCmd.Parse(os.Args[2:])

		args := buildCmd.Args()
		if *tag == "" || len(args) < 1 {
			fmt.Println("usage: docksmith build -t <name:tag> <context>")
			os.Exit(1)
		}

		context := args[0]

		parts := strings.Split(*tag, ":")
		if len(parts) != 2 {
			fmt.Println("invalid tag format, expected name:tag")
			os.Exit(1)
		}
		name, tagVal := parts[0], parts[1]

		instructions, err := parseDocksmithfile(filepath.Join(context, "Docksmithfile"))
		if err != nil {
			fmt.Println("parse error:", err)
			os.Exit(1)
		}

		opts := BuildOptions{NoCache: *noCache, Name: name, Tag: tagVal}
		totalStart := time.Now()

		buildState, created, err := executeInstructions(instructions, context, state, opts)
		if err != nil {
			fmt.Println("build error:", err)
			os.Exit(1)
		}

		// Store only the build-specific layers in the manifest.
		// Base image content is reconstructed at runtime via loadBaseImage.
		// BaseLayers are recorded separately for cache key chaining only.
		m := ImageManifest{
			Name:      name,
			Tag:       tagVal,
			BaseImage: buildState.BaseImage,
			Digest:    "",
			Created:   created,
			Config: Config{
				Env:        buildState.Env,
				Cmd:        buildState.Cmd,
				WorkingDir: buildState.WorkingDir,
			},
			Layers: buildState.Layers,
		}

		digest, err := computeManifestDigest(&m)
		if err != nil {
			fmt.Println("error computing digest:", err)
			os.Exit(1)
		}
		m.Digest = digest

		if err := saveManifest(state, &m); err != nil {
			fmt.Println("error saving manifest:", err)
			os.Exit(1)
		}

		shortDigest := digest
		if strings.HasPrefix(shortDigest, "sha256:") {
			shortDigest = shortDigest[7:]
		}
		if len(shortDigest) > 12 {
			shortDigest = shortDigest[:12]
		}

		totalElapsed := time.Since(totalStart)
		fmt.Printf("Successfully built %s %s:%s (%.2fs)\n", shortDigest, name, tagVal, totalElapsed.Seconds())

	// ------------------------------------------------------------------- run
	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		var envs EnvList
		runCmd.Var(&envs, "e", "env variables (KEY=VALUE)")
		runCmd.Parse(os.Args[2:])

		args := runCmd.Args()
		if len(args) < 1 {
			fmt.Println("usage: docksmith run <name:tag> [cmd]")
			os.Exit(1)
		}

		image := args[0]
		cmd := args[1:]

		parts := strings.Split(image, ":")
		if len(parts) != 2 {
			fmt.Println("invalid image format, expected name:tag")
			os.Exit(1)
		}

		filename := parts[0] + "_" + parts[1]
		m, err := loadManifest(state, filename)
		if err != nil {
			fmt.Println("error loading image:", err)
			os.Exit(1)
		}

		tmpDir, err := os.MkdirTemp("", "docksmith-rootfs-*")
		if err != nil {
			fmt.Println("error creating temp dir:", err)
			os.Exit(1)
		}
		defer os.RemoveAll(tmpDir)

		// Assemble filesystem: base image then layers.
		if err := loadBaseImage(m.BaseImage, state, tmpDir); err != nil {
			fmt.Println("error loading base image:", err)
			os.Exit(1)
		}
		if err := extractAllLayers(m.Layers, state, tmpDir); err != nil {
			fmt.Println("error extracting layers:", err)
			os.Exit(1)
		}

		workdir := m.Config.WorkingDir
		if workdir == "" {
			workdir = "/"
		}
		if err := os.MkdirAll(filepath.Join(tmpDir, workdir), 0755); err != nil {
			fmt.Println("error creating workdir:", err)
			os.Exit(1)
		}

		// Merge env: image ENV first, then -e overrides.
		finalEnv := append([]string{}, m.Config.Env...)
		for _, e := range envs {
			key := strings.SplitN(e, "=", 2)[0]
			var filtered []string
			for _, existing := range finalEnv {
				if !strings.HasPrefix(existing, key+"=") {
					filtered = append(filtered, existing)
				}
			}
			finalEnv = append(filtered, e)
		}

		// quoteArg single-quotes a shell argument safely.
		quoteArg := func(a string) string {
			return "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
		}

		var runErr error
		if len(cmd) > 0 {
			// CLI override — quote each arg and wrap in shell string.
			var quoted []string
			for _, a := range cmd {
				quoted = append(quoted, quoteArg(a))
			}
			cmdStr := "cd " + workdir + " && exec " + strings.Join(quoted, " ")
			runErr = runCommandChroot(tmpDir, []string{cmdStr}, finalEnv)
		} else if len(m.Config.Cmd) > 0 {
			// Exec form from image CMD — quote each arg and wrap in shell string.
			var quoted []string
			for _, a := range m.Config.Cmd {
				quoted = append(quoted, quoteArg(a))
			}
			cmdStr := "cd " + workdir + " && exec " + strings.Join(quoted, " ")
			runErr = runCommandChroot(tmpDir, []string{cmdStr}, finalEnv)
		} else {
			fmt.Println("error: no command specified and no CMD in image")
			os.Exit(1)
		}

		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				code := exitErr.ExitCode()
				fmt.Printf("Container exited with code %d\n", code)
				os.Exit(code)
			}
			fmt.Println("error running container:", runErr)
			os.Exit(1)
		}
		fmt.Println("Container exited with code 0")

	// ----------------------------------------------------------------- images
	case "images":
		if err := listImages(state); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}

	// -------------------------------------------------------------------- rmi
	case "rmi":
		if len(os.Args) < 3 {
			fmt.Println("usage: docksmith rmi <name:tag>")
			os.Exit(1)
		}
		if err := removeImage(state, os.Args[2]); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("Removed", os.Args[2])

	default:
		fmt.Println("unknown command:", os.Args[1])
		os.Exit(1)
	}
}
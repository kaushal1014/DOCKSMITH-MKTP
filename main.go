package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type EnvList []string

func (e *EnvList) String() string {
	return ""
}

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

		// parse name:tag
		parts := strings.Split(*tag, ":")
		if len(parts) != 2 {
			fmt.Println("invalid tag format, expected name:tag")
			os.Exit(1)
		}

		name := parts[0]
		tagVal := parts[1]

		fmt.Println("Building", name+":"+tagVal, "context:", context, "no-cache:", *noCache)

		// 1. Parse Docksmithfile
		instructions, err := parseDocksmithfile("Docksmithfile")
		if err != nil {
			fmt.Println("parse error:", err)
			os.Exit(1)
		}

		// 2. Execute instructions (this handles COPY layers now)
		buildState, err := executeInstructions(instructions, context, state)
		if err != nil {
			fmt.Println("build error:", err)
			os.Exit(1)
		}

		// debug (optional)
		fmt.Println("Final Build State:")
		fmt.Println("Base:", buildState.BaseImage)
		fmt.Println("Workdir:", buildState.WorkingDir)
		fmt.Println("Env:", buildState.Env)
		fmt.Println("Cmd:", buildState.Cmd)
		fmt.Println("Layers:", len(buildState.Layers))

		// 3. Create manifest from build state
		m := ImageManifest{
			Name:    name,
			Tag:     tagVal,
			Digest:  "",
			Created: time.Now().Format(time.RFC3339),
			Config: Config{
				Env:        buildState.Env,
				Cmd:        buildState.Cmd,
				WorkingDir: buildState.WorkingDir,
			},
			Layers: buildState.Layers,
		}

		// 4. Compute digest
		digest, err := computeManifestDigest(&m)
		if err != nil {
			fmt.Println("error computing digest:", err)
			os.Exit(1)
		}
		m.Digest = digest

		// 5. Save manifest
		err = saveManifest(state, &m)
		if err != nil {
			fmt.Println("error saving manifest:", err)
			os.Exit(1)
		}

		fmt.Println("Successfully built", name+":"+tagVal)


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

		fmt.Println("RUN:", image, "cmd:", cmd, "env:", envs, "state:", state.Root)

	case "images":
		err := listImages(state)
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}

	case "rmi":
		if len(os.Args) < 3 {
			fmt.Println("usage: docksmith rmi <name:tag>")
			os.Exit(1)
		}
		err := removeImage(state, os.Args[2])
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}

fmt.Println("Removed", os.Args[2])

	default:
		fmt.Println("unknown command")
		os.Exit(1)
	}
}

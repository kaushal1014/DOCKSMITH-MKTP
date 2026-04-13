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
		
		parts := strings.Split(*tag, ":")
		if len(parts) != 2 {
			fmt.Println("invalid tag format, expected name:tag")
			os.Exit(1)
		}

		name := parts[0]
		tagVal := parts[1]
		
		// create minimal manifest (dummy build for now)
		m := ImageManifest{
			Name:    name,
			Tag:     tagVal,
			Digest:  "", // will compute later
			Created: time.Now().Format(time.RFC3339),
			Config: Config{
				Env:        []string{},
				Cmd:        []string{},
				WorkingDir: "",
			},
			Layers: []Layer{},
		}
		digest, err := computeManifestDigest(&m)
		if err != nil {
			fmt.Println("error computing digest:", err)
			os.Exit(1)
		}

		m.Digest = digest
		// save it
		err = saveManifest(state, &m)
		if err != nil {
			fmt.Println("error saving manifest:", err)
			os.Exit(1)
		}

		// final output
		fmt.Println("Building", name+":"+tagVal, "context:", context, "no-cache:", *noCache)


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

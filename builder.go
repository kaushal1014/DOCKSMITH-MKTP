package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type BuildState struct {
	BaseImage string

	Env        []string
	Cmd        []string
	WorkingDir string
	RootFS string

	Layers []Layer // empty for now
}

func executeInstructions(instructions []Instruction, context string, state *State) (*BuildState, error) {
	buildState := &BuildState{}

	for _, inst := range instructions {
		switch inst.Type {

		case "FROM":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("FROM requires exactly 1 argument")
			}

			base := inst.Args[0]
			buildState.BaseImage = base

			rootfs, err := os.MkdirTemp("", "docksmith-build-*")
			if err != nil {
				return nil, err
			}

			buildState.RootFS = rootfs

			err = loadBaseImage(base, state, rootfs)
			if err != nil {
				return nil, err
			}

			fmt.Println("Loaded base image:", base)
			fmt.Println("RootFS:", rootfs)

		case "WORKDIR":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("WORKDIR requires exactly 1 argument")
			}
			buildState.WorkingDir = inst.Args[0]

		case "ENV":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("ENV requires KEY=VALUE")
			}
			buildState.Env = append(buildState.Env, inst.Args[0])

		case "CMD":
			cmd, err := parseCMD(inst.Args)
			if err != nil {
				return nil, err
			}
			buildState.Cmd = cmd

		case "COPY":
			if len(inst.Args) != 2 {
				return nil, fmt.Errorf("COPY requires src and dest")
			}

			src := inst.Args[0]
			dest := inst.Args[1]

			if src != "." {
				return nil, fmt.Errorf("only COPY . supported for now")
			}

			// 1. APPLY to rootfs 🔥
			err := copyToRootFS(context, dest, buildState.RootFS)
			if err != nil {
				return nil, err
			}
			fmt.Println("Applying COPY to rootfs:", buildState.RootFS)

			// 2. CREATE layer (existing logic)
			layer, err := createCopyLayer(context, dest, state)
			if err != nil {
				return nil, err
			}

			buildState.Layers = append(buildState.Layers, layer)

			fmt.Println("Created COPY layer:", layer.Digest)
		case "RUN":
			fmt.Println("RUN step (not implemented yet)")
		}
	}

	return buildState, nil
}

func parseCMD(args []string) ([]string, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("CMD must be in JSON array form")
	}

	var cmd []string
	err := json.Unmarshal([]byte(args[0]), &cmd)
	if err != nil {
		return nil, fmt.Errorf("invalid CMD format, must be JSON array")
	}

	return cmd, nil
}

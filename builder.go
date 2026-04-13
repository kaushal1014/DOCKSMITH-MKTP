package main

import (
	"encoding/json"
	"fmt"
)

type BuildState struct {
	BaseImage string

	Env        []string
	Cmd        []string
	WorkingDir string

	Layers []Layer // empty for now
}

func executeInstructions(instructions []Instruction) (*BuildState, error) {
	state := &BuildState{}

	for _, inst := range instructions {
		switch inst.Type {

		case "FROM":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("FROM requires exactly 1 argument")
			}
			state.BaseImage = inst.Args[0]

		case "WORKDIR":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("WORKDIR requires exactly 1 argument")
			}
			state.WorkingDir = inst.Args[0]

		case "ENV":
			if len(inst.Args) != 1 {
				return nil, fmt.Errorf("ENV requires KEY=VALUE")
			}
			state.Env = append(state.Env, inst.Args[0])

		case "CMD":
			cmd, err := parseCMD(inst.Args)
			if err != nil {
				return nil, err
			}
			state.Cmd = cmd

		case "COPY":
			fmt.Println("COPY step (not implemented yet)")

		case "RUN":
			fmt.Println("RUN step (not implemented yet)")
		}
	}

	return state, nil
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

package main

import (
	"os"
	"path/filepath"
)

type State struct {
	Root   string
	Images string
	Layers string
	Cache  string
}

func initState() (*State, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	root := filepath.Join(home, ".docksmith")

	state := &State{
		Root:   root,
		Images: filepath.Join(root, "images"),
		Layers: filepath.Join(root, "layers"),
		Cache:  filepath.Join(root, "cache"),
	}

	dirs := []string{state.Images, state.Layers, state.Cache}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	return state, nil
}

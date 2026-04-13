package main

import (
	"os"
	"os/exec"
)

func runCommandChroot(root string, cmdArgs []string) error {
	if len(cmdArgs) == 0 {
		return nil
	}

	// build command: chroot <root> <cmd> ...
	args := append([]string{root}, cmdArgs...)

	cmd := exec.Command("chroot", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

package main

import (
	"os"
	"os/exec"
)

func runCommandChroot(root string, cmdArgs []string, envs []string) error {
	if len(cmdArgs) == 0 {
		return nil
	}

	// sudo chroot <root> env -i KEY=VAL ... /bin/sh -c <cmd>
	// -i clears the inherited environment so only declared ENV vars are present.
	// sudo strips cmd.Env, so we pass vars via the `env` utility inside the chroot.
	args := []string{"chroot", root, "env", "-i"}
	args = append(args, envs...)

	if len(cmdArgs) == 1 {
		args = append(args, "/bin/sh", "-c", cmdArgs[0])
	} else {
		args = append(args, cmdArgs...)
	}

	cmd := exec.Command("sudo", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
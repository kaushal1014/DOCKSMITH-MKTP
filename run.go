package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func runCommandChroot(root string, cmdArgs []string, envs []string) error {
	if len(cmdArgs) == 0 {
		return nil
	}

	var args []string
	if len(cmdArgs) == 1 {
		args = []string{"/bin/sh", "-c", cmdArgs[0]}
	} else {
		args = cmdArgs
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = envs

	cmd.SysProcAttr = &unix.SysProcAttr{
		Cloneflags: unix.CLONE_NEWNS |
			unix.CLONE_NEWPID |
			unix.CLONE_NEWUTS |
			unix.CLONE_NEWIPC |
			unix.CLONE_NEWNET,
		Chroot: root,
	}

	return cmd.Run()
}
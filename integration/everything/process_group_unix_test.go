//go:build integration && !windows

package everything_test

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	syscallSignal, ok := signal.(syscall.Signal)
	if !ok {
		return process.Signal(signal)
	}
	return syscall.Kill(-process.Pid, syscallSignal)
}

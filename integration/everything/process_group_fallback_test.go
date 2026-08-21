//go:build integration && windows

package everything_test

import (
	"os"
	"os/exec"
)

func configureProcessGroup(*exec.Cmd) {}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	return process.Signal(signal)
}

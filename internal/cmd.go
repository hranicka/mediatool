// Package internal provides internal functions for the application.
package internal

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
)

func RunCmd(name string, arg ...string) ([]byte, error) {
	var cmdOut bytes.Buffer
	var cmdErr bytes.Buffer
	cmd := exec.Command(name, arg...)
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdErr

	slog.Debug("running command", "cmd", cmd.String())
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, cmdErr.String())
	}
	return cmdOut.Bytes(), nil
}

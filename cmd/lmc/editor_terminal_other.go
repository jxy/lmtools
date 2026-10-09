//go:build !darwin && !freebsd && !linux

package main

import (
	"context"
	"errors"
	"os/exec"
)

func (*inputOwner) runEditor(context.Context, *exec.Cmd, func(bool)) error {
	return errors.New("editor terminal handoff is not supported on this platform")
}

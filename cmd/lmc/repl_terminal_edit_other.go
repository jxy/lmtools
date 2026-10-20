//go:build !darwin && !freebsd && !linux

package main

import "io"

func (*inputOwner) enableEditing(io.Writer, *replHistory) (bool, error) { return false, nil }
func (*inputOwner) disableEditing() error                               { return nil }

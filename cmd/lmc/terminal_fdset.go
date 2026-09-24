//go:build darwin || linux

package main

import (
	"syscall"
	"unsafe"
)

// fdSetBits is how many descriptors one element of a select set holds.
const fdSetBits = int(unsafe.Sizeof(syscall.FdSet{}.Bits[0])) * 8

func fdSetAdd(set *syscall.FdSet, fd int) {
	set.Bits[fd/fdSetBits] |= 1 << uint(fd%fdSetBits)
}

func fdSetHas(set *syscall.FdSet, fd int) bool {
	return set.Bits[fd/fdSetBits]&(1<<uint(fd%fdSetBits)) != 0
}

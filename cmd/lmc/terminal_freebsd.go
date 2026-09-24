package main

import (
	"bytes"
	"errors"
	"syscall"
	"unsafe"
)

// fiodgnameArg is struct fiodgname_arg from sys/filio.h.
type fiodgnameArg struct {
	len int32
	buf unsafe.Pointer
}

// fiodgname is FIODGNAME, _IOW('f', 120, struct fiodgname_arg), which the
// syscall package omits. The request encodes the size of its argument,
// which depends on the pointer size: 0x80106678 on amd64.
const fiodgname = 0x80000000 | (unsafe.Sizeof(fiodgnameArg{})&0x1fff)<<16 | 'f'<<8 | 120

// terminalName asks the device on fd for its name, which any terminal
// device has, consoles and serial lines as well as pseudo terminals.
func terminalName(fd int) (string, error) {
	var name [256]byte
	arg := fiodgnameArg{len: int32(len(name)), buf: unsafe.Pointer(&name[0])}
	if err := terminalIoctl(fd, fiodgname, unsafe.Pointer(&arg)); err != nil {
		return "", err
	}
	end := bytes.IndexByte(name[:], 0)
	if end <= 0 {
		return "", errors.New("FIODGNAME returned no name")
	}
	return "/dev/" + string(name[:end]), nil
}

// fdSetBits is how many descriptors one element of a select set holds.
const fdSetBits = int(unsafe.Sizeof(syscall.FdSet{}.X__fds_bits[0])) * 8

func fdSetAdd(set *syscall.FdSet, fd int) {
	set.X__fds_bits[fd/fdSetBits] |= 1 << uint(fd%fdSetBits)
}

func fdSetHas(set *syscall.FdSet, fd int) bool {
	return set.X__fds_bits[fd/fdSetBits]&(1<<uint(fd%fdSetBits)) != 0
}

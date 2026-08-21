//go:build linux

package main

import "syscall"

const terminalReadTermios = syscall.TCGETS

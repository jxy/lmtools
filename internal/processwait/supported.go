//go:build linux || darwin || (freebsd && (amd64 || arm64 || riscv64))

package processwait

// Supported reports whether this platform can observe an exit without reaping.
const Supported = true

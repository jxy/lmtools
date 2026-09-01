package constants

import (
	"slices"
	"strings"
)

// PermittedCommandDevicesText is both the user-facing list and the source for
// exact membership checks. The list is explicit because arbitrary character
// devices include unsafe targets such as raw disks; /dev/tty is also excluded
// because it can overwrite lmc's terminal UI.
const PermittedCommandDevicesText = "/dev/null, /dev/zero, /dev/full, /dev/random, /dev/urandom"

var permittedCommandDevices = strings.Split(PermittedCommandDevicesText, ", ")

// IsPermittedCommandDeviceName checks the exact, uncleaned path. Membership is
// necessary but not sufficient; the caller must also verify a character device.
func IsPermittedCommandDeviceName(path string) bool {
	return slices.Contains(permittedCommandDevices, path)
}

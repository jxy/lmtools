// Package processwait observes child exit without releasing its process ID.
// Callers can finish signaling the child's process group before reaping it
// through exec.Cmd.Wait. A stopped child has not exited.
package processwait

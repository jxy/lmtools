package alcfauth

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var errNoBrowser = errors.New("no browser to open")

// OpenBrowser starts a browser on url without waiting for it: the first
// command in $BROWSER when set, otherwise open on macOS and xdg-open on other
// systems with a display. It opens nothing in an SSH session without
// $BROWSER, or on a system without a display, where xdg-open could start a
// text browser on the terminal the code is about to be pasted into.
func OpenBrowser(url string) error {
	name, args := browserCommand(os.Getenv, runtime.GOOS)
	if name == "" {
		return errNoBrowser
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, append(args, url)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func browserCommand(getenv func(string) string, goos string) (string, []string) {
	if browser := getenv("BROWSER"); browser != "" {
		fields := strings.Fields(strings.Split(browser, ":")[0])
		if len(fields) == 0 {
			return "", nil
		}
		return fields[0], fields[1:]
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return "", nil
	}
	if goos == "darwin" {
		return "open", nil
	}
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return "", nil
	}
	return "xdg-open", nil
}

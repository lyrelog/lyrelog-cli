package login

import (
	"os/exec"
	"runtime"
)

// CanOpenBrowser: false over SSH and on a Linux without a display, where a browser would open
// somewhere the person can't see (or not at all).
func CanOpenBrowser(getenv func(string) string) bool {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" || getenv("SSH_CLIENT") != "" {
		return false
	}
	if getenv("BROWSER") == "none" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
	}
}

// OpenBrowser opens a URL with $BROWSER or the system's opener. The URL is an argument, never
// shell text.
func OpenBrowser(getenv func(string) string, target string) error {
	var cmd *exec.Cmd
	switch b := getenv("BROWSER"); {
	case b != "":
		cmd = exec.Command(b, target)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", target)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

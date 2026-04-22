package auth

import (
	"os/exec"
	"runtime"
)

// openBrowser attempts to open the given URL in the default system browser.
func openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	default:
		return
	}

	_ = exec.Command(cmd, args...).Start()
}

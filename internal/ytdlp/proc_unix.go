//go:build unix

package ytdlp

import (
	"os/exec"
	"syscall"
)

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A negative pid signals the whole group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

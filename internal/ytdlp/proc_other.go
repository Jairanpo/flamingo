//go:build !unix

package ytdlp

import "os/exec"

// Elsewhere (Windows) only WaitDelay applies.
func ownGroup(cmd *exec.Cmd) {}

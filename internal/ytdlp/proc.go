package ytdlp

import (
	"context"
	"os/exec"
	"time"
)

// command starts yt-dlp so that a deadline really ends it. The standalone
// yt-dlp is a bundled Python program that re-launches itself as a child:
// killing only the process Go started left the child running and holding
// the output pipe, and Run waited on that pipe forever. So yt-dlp runs in a
// process group of its own (on Unix) and the whole group is killed, and
// WaitDelay stops waiting for the pipes a few seconds after that anyway.
func (r Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.path(), args...)
	ownGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

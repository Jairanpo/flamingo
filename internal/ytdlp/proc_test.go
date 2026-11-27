//go:build unix

package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A yt-dlp that leaves a child holding its output, as the bundled build
// does, must still give up at the deadline rather than wait on the pipe.
func TestDeadlineEndsChildren(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := Runner{Path: fake}.lines(ctx, "-j")
	if err == nil {
		t.Fatal("no error at the deadline")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s to give up", d)
	}
}

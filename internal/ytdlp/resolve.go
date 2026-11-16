// Package ytdlp runs the yt-dlp program: listings for the catalog, and
// owner lookups for the importer.
package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	Path      string
	ExtraArgs []string
}

// Owner is the channel behind a playlist or channel URL.
type Owner struct {
	ChannelID string
	Name      string
	Handle    string // "@name", or "" when YouTube gave none
}

// PlaylistOwner asks YouTube who owns a playlist.
func (r Runner) PlaylistOwner(ctx context.Context, playlistID string) (Owner, error) {
	return r.owner(ctx, "https://www.youtube.com/playlist?list="+playlistID)
}

// Channel looks up a channel's name and handle from its id.
func (r Runner) Channel(ctx context.Context, channelID string) (Owner, error) {
	return r.owner(ctx, "https://www.youtube.com/channel/"+channelID+"/videos")
}

func (r Runner) owner(ctx context.Context, url string) (Owner, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// -I 0 lists no entries: only the list's own metadata comes back.
	args := append(r.base(), "--flat-playlist", "-I", "0", "-J")
	out, err := exec.CommandContext(ctx, r.path(), append(args, url)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return Owner{}, fmt.Errorf("yt-dlp %s: %s", url, strings.TrimSpace(string(ee.Stderr)))
		}
		return Owner{}, fmt.Errorf("yt-dlp %s: %w", url, err)
	}
	var meta struct {
		ChannelID  string `json:"channel_id"`
		Channel    string `json:"channel"`
		UploaderID string `json:"uploader_id"`
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return Owner{}, fmt.Errorf("yt-dlp %s: unreadable output: %w", url, err)
	}
	if meta.ChannelID == "" {
		return Owner{}, fmt.Errorf("yt-dlp %s: no channel id in the answer", url)
	}
	o := Owner{ChannelID: meta.ChannelID, Name: meta.Channel}
	if strings.HasPrefix(meta.UploaderID, "@") {
		o.Handle = meta.UploaderID
	}
	return o, nil
}

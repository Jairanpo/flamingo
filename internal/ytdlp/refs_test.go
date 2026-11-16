package ytdlp

import (
	"errors"
	"testing"
)

func TestSourceURL(t *testing.T) {
	for in, want := range map[string]string{
		"@alpha":                             "https://www.youtube.com/@alpha/videos",
		"alpha":                              "https://www.youtube.com/@alpha/videos",
		"https://www.youtube.com/@a/":        "https://www.youtube.com/@a/videos",
		"https://www.youtube.com/@a/streams": "https://www.youtube.com/@a/streams",
		"https://www.youtube.com/@a/courses": "https://www.youtube.com/@a/courses",
		"PLabcdef":                           "https://www.youtube.com/playlist?list=PLabcdef",
		"https://www.youtube.com/playlist?list=PLabcdef":           "https://www.youtube.com/playlist?list=PLabcdef",
		"https://youtu.be/aaaaaaaaaa1":                             PicksURL + "aaaaaaaaaa1",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&t=3":          PicksURL + "aaaaaaaaaa1",
		"https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PLx1234": "https://www.youtube.com/watch?v=aaaaaaaaaa1&list=PLx1234",
	} {
		if got := SourceURL(in); got != want {
			t.Errorf("SourceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanRefs(t *testing.T) {
	ok := []string{"@alpha", "https://www.youtube.com/@alpha", "www.youtube.com/@alpha", "PLabcdef",
		"https://youtu.be/aaaaaaaaaa1"}
	for _, r := range ok {
		if _, err := CleanSourceRef(r); err != nil {
			t.Errorf("CleanSourceRef(%q): %v", r, err)
		}
	}
	bad := []string{"", "  ", "https://evil.example/@alpha", "file:///etc/passwd", "a b c",
		"https://www.youtube.com/@alpha/playlists", "https://www.youtube.com/@alpha/courses"}
	for _, r := range bad {
		if _, err := CleanSourceRef(r); !errors.Is(err, ErrRef) {
			t.Errorf("CleanSourceRef(%q) = %v, want ErrRef", r, err)
		}
	}
	if _, err := CleanCreatorRef("PLabcdef"); !errors.Is(err, ErrRef) {
		t.Errorf("a playlist is not a creator: %v", err)
	}
	if _, err := CleanCreatorRef("https://www.youtube.com/@alpha/playlists"); err != nil {
		t.Errorf("a creator's playlists tab is browsable: %v", err)
	}
}

func TestPicksAndLive(t *testing.T) {
	got := PickedIDs(PicksURL + "aaaaaaaaaa1,bad,aaaaaaaaaa2")
	if len(got) != 2 || got[1] != "aaaaaaaaaa2" {
		t.Errorf("PickedIDs = %v", got)
	}
	cid := "UCaaaaaaaaaaaaaaaaaaaaaa"
	if ChannelOfLive(LivePlaylistID(cid)) != cid {
		t.Errorf("live round trip")
	}
}

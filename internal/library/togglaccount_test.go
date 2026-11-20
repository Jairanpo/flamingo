package library

import "testing"

func TestTogglTag(t *testing.T) {
	for in, want := range map[string]string{
		" Study ":            "study",
		"ClickUp":            "clickup",
		"youtube":            "Youtube",
		"YOUTUBE":            "Youtube",
		"youtube:Ali Abdaal": "Youtube:Ali Abdaal",
		"Youtube:Ali Abdaal": "Youtube:Ali Abdaal",
		"youtubers":          "youtubers",
		"":                   "",
	} {
		if got := TogglTag(in); got != want {
			t.Errorf("TogglTag(%q) = %q, want %q", in, got, want)
		}
	}
}

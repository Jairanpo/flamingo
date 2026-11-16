package ytdlp

import (
	"encoding/json"
	"errors"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPickStream(t *testing.T) {
	info := decode(t, `{
	  "duration": 754,
	  "chapters": [{"start_time": 0, "end_time": 61.234, "title": "Intro"},
	               {"start_time": 61.234, "end_time": 754, "title": ""}],
	  "formats": [
	    {"url": "u360", "ext": "mp4", "protocol": "https", "vcodec": "avc1.4d401e", "acodec": "none", "height": 360, "fps": 30},
	    {"url": "u720a", "ext": "mp4", "protocol": "https", "vcodec": "av01.0.05M.08", "acodec": "none", "height": 720, "fps": 60},
	    {"url": "u720h", "ext": "mp4", "protocol": "https", "vcodec": "avc1.4d401f", "acodec": "none", "height": 720, "fps": 30},
	    {"url": "u1080-30", "ext": "mp4", "protocol": "https", "vcodec": "avc1.640028", "acodec": "none", "height": 1080, "fps": 30},
	    {"url": "u1080-60", "ext": "mp4", "protocol": "https", "vcodec": "avc1.64002a", "acodec": "none", "height": 1080, "fps": 60},
	    {"url": "u2160hdr", "ext": "mp4", "protocol": "https", "vcodec": "av01.0.12M.10", "acodec": "none", "height": 2160, "dynamic_range": "HDR10"},
	    {"url": "u2160sdr", "ext": "mp4", "protocol": "https", "vcodec": "av01.0.12M.08", "acodec": "none", "height": 2160, "dynamic_range": "SDR"},
	    {"url": "webm", "ext": "webm", "protocol": "https", "vcodec": "vp9", "acodec": "none", "height": 1440},
	    {"url": "hls", "ext": "mp4", "protocol": "m3u8_native", "vcodec": "avc1", "acodec": "none", "height": 480},
	    {"url": "muxed", "ext": "mp4", "protocol": "https", "vcodec": "avc1", "acodec": "mp4a.40.2", "height": 240}
	  ],
	  "requested_formats": [
	    {"url": "v", "vcodec": "avc1.64002a", "acodec": "none"},
	    {"url": "a", "vcodec": "none", "acodec": "mp4a.40.2", "http_headers": {"User-Agent": "x"}}
	  ]
	}`)
	st, err := pickStream(info, 1080)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{360: "u360", 720: "u720h", 1080: "u1080-60", 2160: "u2160sdr"}
	if len(st.Videos) != len(want) {
		t.Fatalf("heights = %v", st.Videos)
	}
	for h, u := range want {
		if st.Videos[h].URL != u {
			t.Errorf("%dp = %s, want %s", h, st.Videos[h].URL, u)
		}
	}
	if st.Default != 1080 || *st.Duration != 754 {
		t.Errorf("default %d duration %v", st.Default, *st.Duration)
	}
	if st.Audio.URL != "a" || st.Audio.Mime != `audio/mp4; codecs="mp4a.40.2"` || st.Audio.Headers["User-Agent"] != "x" {
		t.Errorf("audio = %+v", st.Audio)
	}
	if st.Videos[720].Mime != `video/mp4; codecs="avc1.4d401f"` {
		t.Errorf("mime = %s", st.Videos[720].Mime)
	}
	if len(st.Chapters) != 2 || st.Chapters[0].End != 61.23 || st.Chapters[1].Title != "" {
		t.Errorf("chapters = %+v", st.Chapters)
	}

	// Below everything on offer: the smallest; zero: also the smallest.
	if st, _ := pickStream(info, 144); st.Default != 360 {
		t.Errorf("default for 144 = %d", st.Default)
	}
	if st, _ := pickStream(info, 100000); st.Default != 2160 {
		t.Errorf("default for any = %d", st.Default)
	}

	if _, err := pickStream(decode(t, `{"formats": [], "requested_formats": []}`), 1080); !errors.Is(err, ErrNoStream) {
		t.Errorf("nothing playable = %v", err)
	}
}

func TestChaptersSkipsJunk(t *testing.T) {
	got := chapters(decode(t, `{"chapters": [{"start_time": 1.005, "end_time": 2, "title": "A"}, "junk"]}`))
	if len(got) != 1 || got[0].Start != 1 || got[0].Title != "A" {
		t.Fatalf("got %+v", got)
	}
	if got := chapters(decode(t, `{}`)); len(got) != 0 || got == nil {
		t.Fatalf("none = %#v", got)
	}
}

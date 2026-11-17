package ytdlp

import (
	"strings"
	"testing"
)

func TestFormatSelector(t *testing.T) {
	if got := FormatSelector(720, true); got != "bestaudio[ext=m4a]/bestaudio" {
		t.Errorf("audio = %s", got)
	}
	if got := FormatSelector(720, false); !strings.HasPrefix(got, "bv*[height<=?720][ext=mp4]+ba[ext=m4a]/") {
		t.Errorf("720 = %s", got)
	}
	if got := FormatSelector(0, false); got != "bv*+ba[ext=m4a]/bv*+ba/b/b" {
		t.Errorf("best = %s", got)
	}
	if got := FormatSelector(2160, false); !strings.HasPrefix(got, "bv*[height<=?2160]+ba[ext=m4a]") {
		t.Errorf("4k = %s", got)
	}
	if downloadSort(1080) != "res,fps,hdr:sdr,proto,vcodec:h264" || downloadSort(0) != "res,fps,hdr:sdr,proto" {
		t.Error("sort")
	}
}

func TestParseProgress(t *testing.T) {
	f, s := ParseProgress("PROG\t50000000\t100000000\t2000000\t75")
	if f == nil || *f != 0.5 || s != " 50.0%  50MB/100MB  2.0MB/s  eta 1m15s" {
		t.Errorf("got %v %q", f, s)
	}
	if f, s := ParseProgress("PROG\tNA\tNA\tNA\tNA"); f != nil || s != "working" {
		t.Errorf("unknown = %v %q", f, s)
	}
}

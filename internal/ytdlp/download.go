package ytdlp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Downloads, run by the agent on the computer that keeps the file.

// FormatSelector picks what to fetch. maxHeight 0 means the best there is.
func FormatSelector(maxHeight int, audio bool) string {
	if audio {
		return "bestaudio[ext=m4a]/bestaudio"
	}
	h := maxHeight
	if h == 0 || h > 1080 {
		// H.264 stops at 1080p, so a sharper picture means VP9 or AV1. Merged
		// into mp4 the browser still plays it; screen recordings with small
		// text are what this is for.
		hq := ""
		if h != 0 {
			hq = fmt.Sprintf("[height<=?%d]", h)
		}
		return fmt.Sprintf("bv*%[1]s+ba[ext=m4a]/bv*%[1]s+ba/b%[1]s/b", hq)
	}
	// mp4/m4a first, so the page's <video> plays it without transcoding.
	return fmt.Sprintf("bv*[height<=?%[1]d][ext=mp4]+ba[ext=m4a]/b[height<=?%[1]d][ext=mp4]/"+
		"bv*[height<=?%[1]d]+ba/b[height<=?%[1]d]/b", h)
}

// downloadSort breaks ties between files of the same size: normal colour
// over HDR, whole files over HLS pieces, and up to 1080p H.264, which every
// machine decodes without effort.
func downloadSort(maxHeight int) string {
	s := "res,fps,hdr:sdr,proto"
	if maxHeight != 0 && maxHeight <= 1080 {
		s += ",vcodec:h264"
	}
	return s
}

// OutputTemplate files each video under its channel, named by date and
// title, with its id in brackets: the id is how the agent finds it again.
func OutputTemplate(mediaDir string) string {
	return filepath.Join(mediaDir,
		"%(channel,uploader|Unknown)s",
		"%(upload_date>%Y-%m-%d,release_date>%Y-%m-%d|0000-00-00)s - %(title).120B [%(id)s].%(ext)s")
}

// Progress reports a download as bytes land: fraction (nil when the size is
// not known yet) and a short human line.
type Progress func(fraction *float64, detail string)

// Download fetches one video, or its sound alone as .m4a, into mediaDir and
// returns the file's path. height caps the picture (0: the best there is).
func (r Runner) Download(ctx context.Context, vid, mediaDir string, audio bool, height int, progress Progress) (string, error) {
	sink, err := os.CreateTemp("", "flamingo-dl-*.tsv")
	if err != nil {
		return "", err
	}
	sink.Close()
	defer os.Remove(sink.Name())

	args := append(r.base(),
		"--no-simulate", "--newline", "--no-playlist",
		"-f", FormatSelector(height, audio),
		"-S", downloadSort(height),
		"-o", OutputTemplate(mediaDir),
		// Asking again at another quality replaces the copy on disk rather
		// than finding it there and stopping.
		"--force-overwrites", "--restrict-filenames", "--no-mtime",
		"--retries", "5", "--fragment-retries", "10",
		"--progress-template",
		"PROG\t%(progress.downloaded_bytes)s\t%(progress.total_bytes,progress.total_bytes_estimate)s\t%(progress.speed)s\t%(progress.eta)s",
		"--print-to-file", "after_move:%(id)s\t%(filepath)s", sink.Name(),
	)
	if audio {
		args = append(args, "-x", "--audio-format", "m4a")
	} else {
		args = append(args, "--merge-output-format", "mp4")
	}
	args = append(args, "https://www.youtube.com/watch?v="+vid)

	cmd := r.command(ctx, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting yt-dlp: %w", err)
	}
	var tail []string
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "PROG\t") {
			if progress != nil {
				progress(ParseProgress(line))
			}
		} else if t := strings.TrimSpace(line); t != "" {
			tail = append(tail, t)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && len(tail) > 0 {
			msg = strings.Join(tail[max(0, len(tail)-5):], "\n")
		}
		if msg == "" {
			msg = "download failed"
		}
		return "", errors.New(clip(msg, 800))
	}
	b, err := os.ReadFile(sink.Name())
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		cols := strings.SplitN(line, "\t", 2)
		if len(cols) == 2 && cols[0] == vid && strings.TrimSpace(cols[1]) != "" {
			return strings.TrimSpace(cols[1]), nil
		}
	}
	return "", errors.New("yt-dlp reported success but named no file")
}

// Subtitles is a best-effort pass after the video is safely on disk. It is
// a run of its own because YouTube rate-limits subtitles hard, and a 429
// here must not cost the download.
func (r Runner) Subtitles(ctx context.Context, vid, mediaDir, langs string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args := append(r.base(),
		"--skip-download", "--no-playlist", "--write-subs", "--write-auto-subs",
		"--sub-langs", langs, "--convert-subs", "vtt",
		"--restrict-filenames", "--no-mtime",
		"-o", OutputTemplate(mediaDir),
		"https://www.youtube.com/watch?v="+vid)
	return r.command(ctx, args...).Run()
}

// ParseProgress reads one PROG line of the progress template.
func ParseProgress(line string) (*float64, string) {
	parts := append(strings.Split(line, "\t"), "NA", "NA", "NA", "NA")
	num := func(s string) float64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	}
	got, total, speed, eta := num(parts[1]), num(parts[2]), num(parts[3]), num(parts[4])
	var frac *float64
	var bits []string
	if got > 0 && total > 0 {
		f := min(got/total, 1)
		frac = &f
		bits = append(bits, fmt.Sprintf("%5.1f%%", f*100))
	}
	if got > 0 {
		s := fmt.Sprintf("%.0fMB", got/1e6)
		if total > 0 {
			s += fmt.Sprintf("/%.0fMB", total/1e6)
		}
		bits = append(bits, s)
	}
	if speed > 0 {
		bits = append(bits, fmt.Sprintf("%.1fMB/s", speed/1e6))
	}
	if eta > 0 {
		bits = append(bits, fmt.Sprintf("eta %dm%02ds", int(eta)/60, int(eta)%60))
	}
	if len(bits) == 0 {
		return frac, "working"
	}
	return frac, strings.Join(bits, "  ")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

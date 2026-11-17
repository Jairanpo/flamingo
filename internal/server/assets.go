package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
)

// The page's files are built into the binary, which has no file dates for a
// browser to compare. Without a fingerprint a browser may keep last week's
// stylesheet after a redeploy. Each file gets a hash of its bytes as its
// ETag, and "no-cache" makes the browser ask every time; an unchanged file
// answers 304 with no body, so asking is cheap.
var assetTags = func() map[string]string {
	tags := map[string]string{}
	_ = fs.WalkDir(webFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(webFS, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		tags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return tags
}()

func assetHandler() http.Handler {
	files := http.FileServerFS(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := assetTags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

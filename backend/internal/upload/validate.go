package upload

import (
	"net/http"
	"path/filepath"
	"strings"
)

var allowedMIMEs = map[string]bool{
	"image/jpeg":       true,
	"image/png":        true,
	"image/webp":       true,
	"image/gif":        true,
	"video/mp4":        true,
	"video/quicktime":  true,
	"video/webm":       true,
	"video/3gpp":       true,
	"video/x-msvideo":  true,
	"video/x-matroska": true,
}

var allowedExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
	".gif":  true,
	".heic": true,
	".heif": true,
	".mov":  true,
	".mp4":  true,
	".m4v":  true,
	".3gp":  true,
	".3g2":  true,
	".avi":  true,
	".mkv":  true,
	".webm": true,
	".hevc": true,
	".mts":  true,
	".m2ts": true,
	".wmv":  true,
	".flv":  true,
}

// SniffMIME detects content type from the first bytes (magic bytes), like uploadHandler did.
func SniffMIME(sniff []byte) string {
	return http.DetectContentType(sniff)
}

// Allowed reports whether the (mime, ext) pair is acceptable.
func Allowed(mime, ext string) bool {
	return allowedMIMEs[mime] || allowedExts[strings.ToLower(ext)]
}

// Ext returns the lower-cased file extension (including dot).
func Ext(name string) string {
	return strings.ToLower(filepath.Ext(name))
}

// Sanitize replaces filesystem-hostile runes in a filename.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

package upload

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

func JSONError(w http.ResponseWriter, msg string, code int) {
	log.Printf("upload error %d: %s", code, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func JSONOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// TokenSource provides a Yandex OAuth access token. nil in mock mode.
type TokenSource interface {
	Token() (string, error)
}

// DiskClient is the seam to the Yandex Disk REST API (or its mock).
// upload depends on this abstraction, not on the concrete yandex package.
type DiskClient interface {
	UploadURL(token, path string) (string, error)
	Put(uploadURL string, body io.Reader, size int64, contentType string) error
}

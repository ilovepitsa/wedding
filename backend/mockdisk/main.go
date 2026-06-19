// mockdisk emulates the Yandex Disk REST API for local development.
//
// Usage:
//
//	go run ./mockdisk          # starts on :9999, saves files to ./uploads/
//
// Then in .env set:
//
//	YANDEX_API_BASE=http://localhost:9999/v1/disk
//	YANDEX_TOKEN=any-value-works
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	port := "9999"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
	uploadsDir := "uploads"
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", uploadsDir, err)
	}

	base := os.Getenv("MOCK_BASE_URL")
	if base == "" {
		base = "http://localhost:" + port
	}

	mux := http.NewServeMux()

	// Implements: GET /v1/disk/resources/upload?path=<remote_path>&overwrite=<bool>
	// Real Yandex Disk returns a signed upload href. We return one pointing at ourselves.
	mux.HandleFunc("/v1/disk/resources/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		remotePath := r.URL.Query().Get("path")
		log.Printf("→ GET upload URL  path=%s", remotePath)

		// Prefer the request Host so the href works inside docker networks
		// where backend reaches mockdisk as http://mockdisk:9999.
		selfBase := base
		if r.Host != "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			selfBase = scheme + "://" + r.Host
		}
		href := selfBase + "/upload?path=" + url.QueryEscape(remotePath)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"href":      href,
			"method":    "PUT",
			"templated": false,
		})
	})

	// Implements: PUT /upload?path=<remote_path>
	// Saves the body as a file in ./uploads/ using only the basename of the remote path.
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		remotePath := r.URL.Query().Get("path")
		localName := sanitize(filepath.Base(remotePath))
		localPath := filepath.Join(uploadsDir, localName)

		f, err := os.Create(localPath)
		if err != nil {
			log.Printf("✗ create %s: %v", localPath, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		n, err := io.Copy(f, r.Body)
		if err != nil {
			log.Printf("✗ write %s: %v", localPath, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("✓ saved  %s  (%s)", localPath, humanSize(n))
		w.WriteHeader(http.StatusCreated)
	})

	// List saved files
	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(uploadsDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, e := range entries {
			info, _ := e.Info()
			fmt.Fprintf(w, "%s  (%s)\n", e.Name(), humanSize(info.Size()))
		}
	})

	log.Printf("mock disk  :%s  uploads → ./%s/", port, uploadsDir)
	log.Printf("set in .env:  YANDEX_API_BASE=http://localhost:%s/v1/disk", port)
	log.Printf("set in .env:  YANDEX_TOKEN=mock  (any non-empty value)")
	log.Printf("list files:   http://localhost:%s/files", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func sanitize(name string) string {
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

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

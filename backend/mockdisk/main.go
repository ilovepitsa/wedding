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
	"log"
	"net/http"
	"os"

	"wedding/internal/mockdisk"
)

func main() {
	port := envOr("PORT", "9999")
	uploadsDir := envOr("UPLOADS_DIR", "uploads")
	log.Printf("mock disk  :%s  uploads → ./%s/", port, uploadsDir)
	log.Fatal(http.ListenAndServe(":"+port, mockdisk.NewHandler(uploadsDir)))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

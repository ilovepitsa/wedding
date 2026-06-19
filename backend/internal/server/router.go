package server

import (
	"context"
	"io"
	"log"
	"net/http"

	"wedding/internal/config"
	"wedding/internal/upload"
	"wedding/internal/yandex"
)

// New builds the HTTP handler. Token manager is created here (nil in mock mode).
func New(cfg config.Config) http.Handler {
	h, _ := build(cfg)
	return h
}

// build is the testable core; it also returns the chunk uploader (nil in simple mode).
func build(cfg config.Config) (http.Handler, *upload.ChunkUploader) {
	var tm *yandex.TokenManager
	if cfg.IsMock() {
		log.Printf("mock mode: API base = %s", cfg.YandexAPIBase)
	} else {
		if cfg.YandexClientID == "" || cfg.YandexClientSecret == "" {
			log.Fatal("YANDEX_CLIENT_ID и YANDEX_CLIENT_SECRET обязательны")
		}
		tm = yandex.NewTokenManager(cfg.YandexClientID, cfg.YandexClientSecret, cfg.YandexRefreshToken)
	}

	dc := yandexDisk{apiBase: yandex.APIBase(cfg.YandexAPIBase)}

	var ts upload.TokenSource
	if tm != nil {
		ts = tm
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/upload", upload.SimpleHandler(cfg, ts, dc))
	mux.HandleFunc("/api/config", upload.ConfigHandler(cfg))
	mux.HandleFunc("/nikita/dasha/14062026/wedding/auth/love/login", yandex.AuthLoginHandler(tm))
	mux.HandleFunc("/nikita/dasha/14062026/wedding/auth/love/submit", yandex.AuthSubmitHandler(tm))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	var uploader *upload.ChunkUploader
	if cfg.UploadMode == "chunked" {
		uploader = upload.NewChunkUploader(cfg.UploadTmpDir, cfg.UploadGCTTL)
		go uploader.GCLoop(context.Background())
		upload.RegisterChunked(mux, cfg, ts, dc, uploader)
	}
	return mux, uploader
}

// yandexDisk adapts the yandex package to upload.DiskClient.
// Defined here (not in yandex) so yandex stays a leaf package with no
// dependency on upload's interface.
type yandexDisk struct{ apiBase string }

func (d yandexDisk) UploadURL(token, path string) (string, error) {
	return yandex.GetUploadURL(d.apiBase, token, path)
}

func (d yandexDisk) Put(uploadURL string, body io.Reader, size int64, contentType string) error {
	return yandex.Put(uploadURL, body, size, contentType)
}

package upload

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"wedding/internal/config"
)

const maxChunkSize = 5 << 20 // 5 MiB hard cap (chunk size is 4 MiB)

// RegisterChunked wires the chunked upload endpoints onto mux.
func RegisterChunked(mux *http.ServeMux, cfg config.Config, ts TokenSource, dc DiskClient, up *ChunkUploader) {
	mux.HandleFunc("/api/upload/chunk", ChunkHandler(up))
	mux.HandleFunc("/api/upload/status", StatusHandler(up))
	mux.HandleFunc("/api/upload/complete", CompleteHandler(cfg, ts, dc, up))
}

func ChunkHandler(up *ChunkUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		id := q.Get("uploadId")
		name := q.Get("name")
		index, _ := strconv.Atoi(q.Get("index"))
		total, _ := strconv.Atoi(q.Get("total"))
		offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
		size, _ := strconv.ParseInt(q.Get("size"), 10, 64)

		body, err := io.ReadAll(io.LimitReader(r.Body, maxChunkSize+1))
		if err != nil {
			JSONError(w, "Ошибка чтения чанка", http.StatusBadRequest)
			return
		}
		if len(body) > maxChunkSize {
			JSONError(w, "Чанк слишком большой", http.StatusRequestEntityTooLarge)
			return
		}

		if err := up.WriteChunk(id, name, index, total, offset, size, body); err != nil {
			if err == errInvalidType {
				JSONError(w, "Разрешены только изображения и видео", http.StatusBadRequest)
				return
			}
			log.Printf("chunk write: %v", err)
			JSONError(w, "Ошибка записи чанка", http.StatusBadRequest)
			return
		}

		received, _, total, _ := up.Status(id)
		JSONOK(w, map[string]any{"received": len(received), "total": total})
	}
}

func StatusHandler(up *ChunkUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("uploadId")
		received, missing, total, ok := up.Status(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		JSONOK(w, map[string]any{
			"total":    total,
			"received": received,
			"missing":  missing,
		})
	}
}

func CompleteHandler(cfg config.Config, ts TokenSource, dc DiskClient, up *ChunkUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("uploadId")
		name := r.URL.Query().Get("name")

		dataPath, size, mime, _, err := up.DataFile(id)
		if err != nil {
			if err.Error() == "upload incomplete" {
				JSONError(w, "Загрузка неполна", http.StatusConflict)
				return
			}
			http.NotFound(w, r)
			return
		}

		var token string
		if ts != nil {
			token, err = ts.Token()
			if err != nil {
				JSONError(w, "Сервис временно недоступен: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}

		filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), Sanitize(name))
		remotePath := cfg.YandexFolder + "/" + filename

		uploadURL, err := dc.UploadURL(token, remotePath)
		if err != nil {
			log.Printf("yd upload url: %v", err)
			JSONError(w, "Ошибка запроса URL для загрузки", http.StatusBadGateway)
			return
		}

		f, err := os.Open(dataPath)
		if err != nil {
			JSONError(w, "Ошибка чтения файла", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		if err := dc.Put(uploadURL, f, size, mime); err != nil {
			log.Printf("yd put: %v", err)
			JSONError(w, "Ошибка загрузки на Яндекс.Диск", http.StatusBadGateway)
			return
		}

		up.Remove(id)
		log.Printf("uploaded %s (%d bytes, chunked)", remotePath, size)
		JSONOK(w, map[string]string{"status": "ok"})
	}
}

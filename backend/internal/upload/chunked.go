package upload

import (
	"io"
	"log"
	"net/http"
	"strconv"
)

const maxChunkSize = 5 << 20 // 5 MiB hard cap (chunk size is 4 MiB)

// RegisterChunked wires the chunked upload endpoints onto mux. The shipper
// (cfg/ts/dc) lives inside the ChunkUploader now; complete just enqueues.
func RegisterChunked(mux *http.ServeMux, up *ChunkUploader) {
	mux.HandleFunc("/api/upload/chunk", ChunkHandler(up))
	mux.HandleFunc("/api/upload/status", StatusHandler(up))
	mux.HandleFunc("/api/upload/complete", CompleteHandler(up))
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
		if len(received) == total {
			log.Printf("chunks complete: %s — все %d чанков получены, жду /complete", id, total)
		}
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

func CompleteHandler(up *ChunkUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("uploadId")

		// Distinguish "incomplete" (409) from "missing" (404): MarkShipping
		// returns false for both, so probe DataFile first for the status code.
		if _, _, _, _, err := up.DataFile(id); err != nil {
			if err.Error() == "upload incomplete" {
				JSONError(w, "Загрузка неполна", http.StatusConflict)
				return
			}
			http.NotFound(w, r)
			return
		}

		if up.MarkShipping(id) {
			log.Printf("complete %s: поставлен в очередь на отгрузку в Диск", id)
			JSONOK(w, map[string]string{"status": "ok"})
			return
		}
		JSONError(w, "Не удалось поставить в очередь", http.StatusInternalServerError)
	}
}

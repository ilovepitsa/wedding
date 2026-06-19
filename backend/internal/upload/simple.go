package upload

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"wedding/internal/config"
)

// SimpleHandler streams a single file straight to Yandex Disk (no local buffering).
// ts is nil in mock mode; dc is the Yandex-backed adapter wired by the server.
func SimpleHandler(cfg config.Config, ts TokenSource, dc DiskClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		origName := r.URL.Query().Get("name")
		if origName == "" {
			JSONError(w, "Параметр «name» обязателен", http.StatusBadRequest)
			return
		}

		sniff := make([]byte, 512)
		n, err := io.ReadFull(r.Body, sniff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			JSONError(w, "Ошибка чтения файла", http.StatusBadRequest)
			return
		}
		sniff = sniff[:n]

		mime := SniffMIME(sniff)
		if !Allowed(mime, Ext(origName)) {
			JSONError(w, "Разрешены только изображения и видео", http.StatusBadRequest)
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

		filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), Sanitize(origName))
		remotePath := cfg.YandexFolder + "/" + filename

		uploadURL, err := dc.UploadURL(token, remotePath)
		if err != nil {
			log.Printf("yd upload url: %v", err)
			JSONError(w, "Ошибка запроса URL для загрузки", http.StatusBadGateway)
			return
		}

		var size int64 = -1
		if r.ContentLength > 0 {
			size = r.ContentLength
		}
		body := io.MultiReader(bytes.NewReader(sniff), r.Body)

		if err := dc.Put(uploadURL, body, size, mime); err != nil {
			log.Printf("yd put: %v", err)
			JSONError(w, "Ошибка загрузки на Яндекс.Диск", http.StatusBadGateway)
			return
		}

		log.Printf("uploaded %s (%d bytes)", remotePath, size)
		JSONOK(w, map[string]string{"status": "ok"})
	}
}

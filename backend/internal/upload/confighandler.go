package upload

import (
	"net/http"

	"wedding/internal/config"
)

// ConfigHandler reports the active upload mode to the frontend.
func ConfigHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		JSONOK(w, map[string]string{"uploadMode": cfg.UploadMode})
	}
}

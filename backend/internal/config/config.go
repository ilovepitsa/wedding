package config

import (
	"os"
	"strings"
	"time"
)

type Config struct {
	Port               string
	UploadMode         string
	UploadTmpDir       string
	UploadGCTTL        time.Duration
	YandexAPIBase      string
	YandexFolder       string
	LogFile            string
	YandexClientID     string
	YandexClientSecret string
	YandexRefreshToken string
}

func Load() Config {
	c := Config{
		Port:               getEnv("PORT", "8080"),
		UploadMode:         strings.ToLower(getEnv("UPLOAD_MODE", "simple")),
		UploadTmpDir:       getEnv("UPLOAD_TMP_DIR", "/app/tmp_uploads"),
		UploadGCTTL:        getDuration("UPLOAD_GC_TTL", 6*time.Hour),
		YandexAPIBase:      os.Getenv("YANDEX_API_BASE"),
		YandexFolder:       strings.TrimRight(getEnv("YANDEX_FOLDER", "/wedding/photos"), "/"),
		LogFile:            getEnv("LOG_FILE", "/app/logs/backend.log"),
		YandexClientID:     os.Getenv("YANDEX_CLIENT_ID"),
		YandexClientSecret: os.Getenv("YANDEX_CLIENT_SECRET"),
		YandexRefreshToken: os.Getenv("YANDEX_REFRESH_TOKEN"),
	}
	if c.UploadMode != "simple" && c.UploadMode != "chunked" {
		c.UploadMode = "simple"
	}
	return c
}

func (c Config) IsMock() bool { return c.YandexAPIBase != "" }

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

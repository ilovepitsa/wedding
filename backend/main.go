package main

import (
	"log"
	"net/http"

	"wedding/internal/config"
	"wedding/internal/server"
)

func main() {
	cfg := config.Load()
	server.SetupLogging(cfg.LogFile)
	log.Printf("listening on :%s (upload mode: %s)", cfg.Port, cfg.UploadMode)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, server.New(cfg)))
}

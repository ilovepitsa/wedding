package server

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// SetupLogging wires stdout + log file.
func SetupLogging(logPath string) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		log.Printf("WARNING: cannot create log dir: %v", err)
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("WARNING: cannot open log file %s: %v", logPath, err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.Printf("logging to %s", logPath)
}

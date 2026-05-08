package main

import (
	"log"
	"os"
	"path/filepath"
)

// debugLog is nil when --debug is not set.
var debugLog *log.Logger

func initDebugLog(projectRoot string) error {
	path := filepath.Join(projectRoot, ".snags", "debug.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	debugLog = log.New(f, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	debugLog.Printf("--- snags started ---")
	return nil
}

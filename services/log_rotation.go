package services

import (
	"log"
	"os"
)

// RotateLogIfNeeded renames path to path+".1" when it grows past maxBytes.
// It runs once at startup, so it never races an active writer.
func RotateLogIfNeeded(path string, maxBytes int64) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxBytes {
		return
	}
	_ = os.Remove(path + ".1")
	if err := os.Rename(path, path+".1"); err != nil {
		log.Printf("[LOG] rotation failed for %s: %v", path, err)
	}
}

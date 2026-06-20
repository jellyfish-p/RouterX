package middleware

import (
	"encoding/json"
	"log"
	"time"
)

func writeStructuredLog(entry map[string]interface{}, fallback func()) {
	if _, ok := entry["ts"]; !ok {
		entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		fallback()
		return
	}
	log.Print(string(raw))
}

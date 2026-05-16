package workerclient

import (
	"encoding/base64"
)

// encodeDataURI builds a "data:text/plain;base64,..." URI from a UTF-8 string.
// The Python worker's storage.fetch_bytes recognises this scheme and returns
// the decoded bytes without touching S3 / network.
func encodeDataURI(text string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	return "data:text/plain;base64," + enc
}

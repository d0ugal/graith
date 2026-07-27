package cipolicy

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:])
}

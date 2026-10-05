package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

// IdentityHashHex16 returns the first 16 hex characters of the SHA-256
// digest of identity. Mirrors singleton.py, which keys lock files on
// hashlib.sha256(session_identity.encode("utf-8")).hexdigest()[:16].
func IdentityHashHex16(identity string) string {
	d := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(d[:8])
}

// FileSessionIdentity derives the identity of a file-backed session:
// "file:" + abs(path), mirroring singleton.session_identity's
// f"file:{os.path.abspath(filename)}".
func FileSessionIdentity(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	return "file:" + abs
}

// StringSessionIdentity derives the identity of a string (in-memory)
// session from its serialized value: "string:" + serialized.
func StringSessionIdentity(serialized string) string {
	return "string:" + serialized
}

// AuthKeySessionIdentity derives the spec's auth-key form: "string:" +
// hex(sha256(authKey)). Keying on the key's digest (not the serialized
// string) lets a Telethon string, a gogram string, and a file session
// holding the same key agree on one identity.
func AuthKeySessionIdentity(authKey []byte) string {
	d := sha256.Sum256(authKey)
	return "string:" + hex.EncodeToString(d[:])
}

// AnonSessionIdentity derives the identity of a sessionless test double:
// "anon:" + pointer address, mirroring f"anon:{id(client)}".
func AnonSessionIdentity(v any) string {
	return fmt.Sprintf("anon:%#x", v)
}

package session

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/amarnathcjd/gogram/telegram"
)

// This file ports the session handling of session_string_generator.py and
// migrate_session.py to Go. The two session formats are incompatible, so the
// migrator unpacks Telethon's StringSession by hand and re-emits it through
// gogram's only public encoder:
//
//   - Telethon (telethon/sessions/string.py, v1) saves
//     "1" + urlsafe_base64(>B{4|16}sH256s) of
//     dc_id (u8) | ip (4 or 16 packed bytes) | port (u16 BE) | auth_key (256
//     bytes). The version '1' is an ASCII byte *outside* the base64 payload.
//     There is no session id, user id, or salt; big-endian throughout.
//   - gogram (telegram.Session.Encode) saves "1BvE" + raw-url-base64 of a
//     JSON object {key, hash, dc_id, ip_addr, app_id}; MTProto.ImportAuth
//     reads key/hash/ip_addr/app_id and drops dc_id (the address drives the
//     DC), and expects hash = sha1(key)[12:20].
//
// Layouts this parser deliberately rejects (they are not Telethon strings):
// an 8-byte header of version + 4-byte session id followed by dc/port/ip/key
// (that shape belongs to other MTProto libraries), and gogram's own legacy
// "1BvX" form.

// ErrNotTelethonStringSession reports that a string is not a Telethon
// StringSession. The migrator never emits a half-parsed key: every structural
// check failure (version byte, base64 payload, address width, key length)
// returns this sentinel, wrapped with the specific reason.
var ErrNotTelethonStringSession = errors.New("not a Telethon StringSession")

const (
	telethonVersion  = '1'
	telethonIPLenV4  = 4
	telethonIPLenV6  = 16
	telethonKeyLen   = 256
	telethonPortLen  = 2
	telethonMinLen   = 1 + telethonIPLenV4 + telethonPortLen + telethonKeyLen // 263
	telethonIPv6Len  = 1 + telethonIPLenV6 + telethonPortLen + telethonKeyLen // 275
	telethonHashFrom = 12
	telethonHashTo   = 20
)

// TelethonSession is the payload a Telethon StringSession carries.
type TelethonSession struct {
	// DCID is Telethon's dc_id (a u8 on the wire).
	DCID uint8
	// IP is the packed DC address (4 or 16 bytes).
	IP net.IP
	// Port is the DC port. gogram owns the DC mapping, so the port is
	// parsed and validated but not forwarded.
	Port uint16
	// AuthKey is the 256-byte MTProto authorization key.
	AuthKey []byte
}

// ParseTelethonString unpacks a Telethon StringSession (v1). The accepted
// shape is the one telethon/sessions/string.py writes:
//
//	'1' + base64.urlsafe_b64encode(struct.pack('>B{}sH256s', dc, ip, port, key))
//
// where ip is 4 or 16 packed bytes; the payload is therefore 263 or 275
// bytes. Anything else — including the 8-byte-header layout of other MTProto
// libraries and gogram's "1BvE"/"1BvX" strings — is rejected with
// ErrNotTelethonStringSession.
func ParseTelethonString(raw string) (*TelethonSession, error) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || s[0] != telethonVersion {
		return nil, fmt.Errorf("%w: expected the version byte '1' first", ErrNotTelethonStringSession)
	}

	// Telethon emits padded urlsafe base64; accepting missing padding too
	// costs nothing and mirrors base64.urlsafe_b64decode's tolerance.
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s[1:], "="))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base64url payload: %v", ErrNotTelethonStringSession, err)
	}
	if len(payload) != telethonMinLen && len(payload) != telethonIPv6Len {
		return nil, fmt.Errorf(
			"%w: payload is %d bytes, want %d (4-byte address) or %d (16-byte address)",
			ErrNotTelethonStringSession, len(payload), telethonMinLen, telethonIPv6Len)
	}

	// Derive the address width from the payload instead of trusting the
	// 352/368-character body heuristic, so a padded or trimmed string still
	// lands on the same split point.
	ipLen := len(payload) - 1 - telethonPortLen - telethonKeyLen
	authKey := make([]byte, telethonKeyLen)
	copy(authKey, payload[1+ipLen+telethonPortLen:])
	if bytes.Equal(authKey, make([]byte, telethonKeyLen)) {
		// Telethon itself treats an all-zero key as "no auth key" and
		// refuses to save such a session; re-emitting it would produce a
		// session gogram cannot connect with.
		return nil, fmt.Errorf("%w: auth_key is all zero", ErrNotTelethonStringSession)
	}

	ip := make(net.IP, ipLen)
	copy(ip, payload[1:1+ipLen])

	return &TelethonSession{
		DCID:    payload[0],
		IP:      ip,
		Port:    binary.BigEndian.Uint16(payload[1+ipLen : 1+ipLen+telethonPortLen]),
		AuthKey: authKey,
	}, nil
}

// GogramSession maps the parsed Telethon fields onto gogram's public
// telegram.Session. Salt stays 0: Telethon's string carries no server salt
// and gogram re-negotiates it on connect. Hash is set explicitly to
// sha1(key)[12:20] — the value MTProto derives for AUTH_KEY_HASH — rather
// than relying on Encode's fill-in fallback, so repeated exports are stable.
//
// appID must be the real api_id: gogram's LoadSession assigns it
// unconditionally, and a 0 would erase the configured value.
func (t *TelethonSession) GogramSession(appID int32) *telegram.Session {
	sum := sha1.Sum(t.AuthKey)
	return &telegram.Session{
		Key:      append([]byte(nil), t.AuthKey...),
		Hash:     sum[telethonHashFrom:telethonHashTo],
		Salt:     0,
		Hostname: t.IP.String(),
		AppID:    appID,
	}
}

// ConvertTelethonString turns one Telethon StringSession into a gogram
// session string ("1BvE..." ) for a client configured with appID.
func ConvertTelethonString(s string, appID int32) (string, error) {
	parsed, err := ParseTelethonString(s)
	if err != nil {
		return "", err
	}
	return parsed.GogramSession(appID).Encode(), nil
}

// WriteGogramSessionFile writes a gogram file session holding the parsed
// Telethon auth material at path (a file path including the .session
// suffix). gogram persists sessions only through its own storage — the file
// is AES-obfuscated JSON, so it must never be hand-written — and
// ImportRawSession is the public entry point that triggers the save. The
// client is constructed without preconnect, updates, or cache, and never
// opens a network connection.
func WriteGogramSessionFile(path string, ts *TelethonSession, appID int32, appHash string) error {
	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:        appID,
		AppHash:      appHash,
		Session:      path,
		NoPreconnect: true,
		NoUpdates:    true,
		DisableCache: true,
		LogLevel:     telegram.LogError,
	})
	if err != nil {
		return fmt.Errorf("creating gogram client: %w", err)
	}
	defer func() { _ = client.Disconnect() }()

	if err := client.ImportRawSession(ts.GogramSession(appID)); err != nil {
		return fmt.Errorf("writing session file %s: %w", path, err)
	}
	return nil
}

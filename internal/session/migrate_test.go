package session

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/amarnathcjd/gogram/telegram"
)

// telethonFixtureV4 is a fixed Telethon StringSession produced by
// telethon/sessions/string.py (v1) for dc_id=2, ip=149.154.167.51,
// port=443, auth_key=bytes 0..255.
const telethonFixtureV4 = "1ApWapzMBuwABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4fICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj9AQUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2BhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5_gIGCg4SFhoeIiYqLjI2Oj5CRkpOUlZaXmJmam5ydnp-goaKjpKWmp6ipqqusra6vsLGys7S1tre4ubq7vL2-v8DBwsPExcbHyMnKy8zNzs_Q0dLT1NXW19jZ2tvc3d7f4OHi4-Tl5ufo6err7O3u7_Dx8vP09fb3-Pn6-_z9_v8="

// telethonFixtureV6 is the same fixture shape with dc_id=4, ip=2001:db8::1,
// port=80, auth_key=bytes 0..255.
const telethonFixtureV6 = "1BCABDbgAAAAAAAAAAAAAAAEAUAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4fICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj9AQUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2BhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5_gIGCg4SFhoeIiYqLjI2Oj5CRkpOUlZaXmJmam5ydnp-goaKjpKWmp6ipqqusra6vsLGys7S1tre4ubq7vL2-v8DBwsPExcbHyMnKy8zNzs_Q0dLT1NXW19jZ2tvc3d7f4OHi4-Tl5ufo6err7O3u7_Dx8vP09fb3-Pn6-_z9_v8="

func fixtureAuthKey() []byte {
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func fixtureHash() []byte {
	sum := sha1.Sum(fixtureAuthKey())
	return sum[12:20]
}

func TestParseTelethonFixtureV4(t *testing.T) {
	if got := len(telethonFixtureV4) - 1; got != 352 {
		t.Fatalf("fixture body is %d chars, want the 352 of a 4-byte-address Telethon string", got)
	}

	parsed, err := ParseTelethonString(telethonFixtureV4)
	if err != nil {
		t.Fatalf("ParseTelethonString: %v", err)
	}
	if parsed.DCID != 2 {
		t.Errorf("DCID = %d, want 2", parsed.DCID)
	}
	if got := parsed.IP.String(); got != "149.154.167.51" {
		t.Errorf("IP = %q, want 149.154.167.51", got)
	}
	if parsed.Port != 443 {
		t.Errorf("Port = %d, want 443", parsed.Port)
	}
	if !bytes.Equal(parsed.AuthKey, fixtureAuthKey()) {
		t.Errorf("AuthKey = %x..., want the 256-byte 0..255 fixture key", parsed.AuthKey[:4])
	}
}

func TestParseTelethonFixtureV6(t *testing.T) {
	if got := len(telethonFixtureV6) - 1; got != 368 {
		t.Fatalf("fixture body is %d chars, want the 368 of a 16-byte-address Telethon string", got)
	}

	parsed, err := ParseTelethonString(telethonFixtureV6)
	if err != nil {
		t.Fatalf("ParseTelethonString: %v", err)
	}
	if parsed.DCID != 4 {
		t.Errorf("DCID = %d, want 4", parsed.DCID)
	}
	if got := parsed.IP.String(); got != "2001:db8::1" {
		t.Errorf("IP = %q, want 2001:db8::1", got)
	}
	if parsed.Port != 80 {
		t.Errorf("Port = %d, want 80", parsed.Port)
	}
	if !bytes.Equal(parsed.AuthKey, fixtureAuthKey()) {
		t.Errorf("AuthKey = %x..., want the 256-byte 0..255 fixture key", parsed.AuthKey[:4])
	}
}

func TestConvertTelethonStringRoundTrip(t *testing.T) {
	converted, err := ConvertTelethonString(telethonFixtureV4, 6)
	if err != nil {
		t.Fatalf("ConvertTelethonString: %v", err)
	}
	if again, _ := ConvertTelethonString(telethonFixtureV4, 6); again != converted {
		t.Errorf("conversion is not stable:\n first %q\nsecond %q", converted, again)
	}
	if !bytes.HasPrefix([]byte(converted), []byte("1BvE")) {
		t.Fatalf("converted string does not start with 1BvE: %q", converted)
	}

	var decoded telegram.Session
	if err := decoded.Decode(converted); err != nil {
		t.Fatalf("gogram Decode: %v", err)
	}
	if !bytes.Equal(decoded.Key, fixtureAuthKey()) {
		t.Errorf("round-tripped key differs from the fixture key")
	}
	if decoded.Hostname != "149.154.167.51" {
		t.Errorf("Hostname = %q, want 149.154.167.51", decoded.Hostname)
	}
	if decoded.AppID != 6 {
		t.Errorf("AppID = %d, want 6", decoded.AppID)
	}
	if !bytes.Equal(decoded.Hash, fixtureHash()) {
		t.Errorf("Hash = %x, want sha1(key)[12:20] = %x", decoded.Hash, fixtureHash())
	}

	// The contract MTProto.ImportAuth parses is the JSON payload, not the
	// struct: assert its shape independently of gogram's decoder.
	payload, err := base64.RawURLEncoding.DecodeString(converted[len("1BvE"):])
	if err != nil {
		t.Fatalf("payload is not raw-url base64: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}
	for _, name := range []string{"key", "hash", "ip_addr", "app_id"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("payload JSON is missing %q", name)
		}
	}
	if got, _ := fields["app_id"].(float64); got != 6 {
		t.Errorf("payload app_id = %v, want 6", fields["app_id"])
	}
}

func TestParseTelethonStringRejections(t *testing.T) {
	gogramString := (&telegram.Session{Key: fixtureAuthKey(), Hostname: "149.154.167.51", AppID: 6}).Encode()

	truncated, _ := base64.RawURLEncoding.DecodeString(telethonFixtureV4[1:])
	shortPayload := base64.RawURLEncoding.EncodeToString(truncated[:250])
	zeroKey := make([]byte, 263)
	zeroKey[0] = 2
	zeroKey[1], zeroKey[2], zeroKey[3], zeroKey[4] = 149, 154, 167, 51
	zeroKey[5], zeroKey[6] = 1, 0xBB

	cases := map[string]string{
		"empty":             "",
		"version only":      "1",
		"wrong version":     "2" + telethonFixtureV4[1:],
		"gogram 1BvE":       gogramString,
		"not base64":        "1!!!!",
		"truncated key":     "1" + shortPayload,
		"all-zero auth key": "1" + base64.RawURLEncoding.EncodeToString(zeroKey),
	}
	for name, input := range cases {
		_, err := ParseTelethonString(input)
		if !errors.Is(err, ErrNotTelethonStringSession) {
			t.Errorf("%s: err = %v, want ErrNotTelethonStringSession", name, err)
		}
	}
}

func TestWriteGogramSessionFileRoundTrip(t *testing.T) {
	parsed, err := ParseTelethonString(telethonFixtureV4)
	if err != nil {
		t.Fatalf("ParseTelethonString: %v", err)
	}

	path := filepath.Join(t.TempDir(), "acct.session")
	if err := WriteGogramSessionFile(path, parsed, 6, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("WriteGogramSessionFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("session file was not written: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("session file is empty")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	if bytes.Contains(raw, parsed.AuthKey) {
		t.Error("session file contains the raw auth key; gogram should have obfuscated it")
	}

	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:        6,
		AppHash:      "0123456789abcdef0123456789abcdef",
		Session:      path,
		NoPreconnect: true,
		NoUpdates:    true,
		DisableCache: true,
		LogLevel:     telegram.LogError,
	})
	if err != nil {
		t.Fatalf("reloading session file: %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	reloaded := client.ExportRawSession()
	if !bytes.Equal(reloaded.Key, parsed.AuthKey) {
		t.Error("reloaded key differs from the fixture key")
	}
	if !bytes.Equal(reloaded.Hash, fixtureHash()) {
		t.Errorf("reloaded Hash = %x, want %x", reloaded.Hash, fixtureHash())
	}
	if reloaded.Hostname != parsed.IP.String() {
		t.Errorf("reloaded Hostname = %q, want %q", reloaded.Hostname, parsed.IP.String())
	}
	if reloaded.AppID != 6 {
		t.Errorf("reloaded AppID = %d, want 6", reloaded.AppID)
	}
}

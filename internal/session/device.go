package session

import (
	"os"
	"strings"

	"github.com/amarnathcjd/gogram/telegram"
)

// Device identity environment variables, mirroring client_identity.py's
// _DEVICE_ENV map. Each variable sets one gogram device field; the Python map
// spells the same pairing with Telethon's constructor keyword names
// (device_model / system_version / app_version), which are the Go field names
// here.
const (
	EnvDeviceModel   = "TELEGRAM_DEVICE_MODEL"
	EnvSystemVersion = "TELEGRAM_SYSTEM_VERSION"
	EnvAppVersion    = "TELEGRAM_APP_VERSION"
)

// DeviceConfig returns the gogram device identity derived from the
// environment. These are the values Telegram lists under Settings > Devices,
// and they are re-sent on every connection, so a long-running server would
// otherwise overwrite the name chosen during login on each reconnect. Setting
// them keeps a stable, recognisable name.
//
// A variable that is unset or blank leaves its field empty, which is exactly
// what client_identity.py does: it passes only the kwargs whose variable is set
// to a non-empty value, so unset ones keep the library's own default rather
// than a value hardcoded here. Empty fields therefore mean "defer to the
// client library's default", not "send an empty device name".
//
// Telethon fills its unset fields from the host platform (for example
// "arm64"); gogram fills them from telegram.DeviceConfig's own defaults.
// Nothing here invents a third set of defaults.
func DeviceConfig() telegram.DeviceConfig {
	return deviceConfigFrom(os.Getenv)
}

// deviceConfigFrom is DeviceConfig with an injectable lookup, so tests can
// supply a map instead of mutating the process environment.
func deviceConfigFrom(getenv func(string) string) telegram.DeviceConfig {
	return telegram.DeviceConfig{
		DeviceModel:   deviceValue(getenv, EnvDeviceModel),
		SystemVersion: deviceValue(getenv, EnvSystemVersion),
		AppVersion:    deviceValue(getenv, EnvAppVersion),
	}
}

// deviceValue reads one identity variable. A blank or whitespace-only value
// counts as unset, matching Python's `if value:` guard on a string that has
// been stripped; surrounding whitespace is trimmed so a stray space in a .env
// line cannot become part of the device name.
func deviceValue(getenv func(string) string, key string) string {
	return strings.TrimSpace(getenv(key))
}

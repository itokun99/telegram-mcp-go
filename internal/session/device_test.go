package session

import (
	"testing"

	"github.com/amarnathcjd/gogram/telegram"
)

// identity is the three device fields the Python side can set.
type identity struct{ model, system, app string }

func identityOf(c telegram.DeviceConfig) identity {
	return identity{c.DeviceModel, c.SystemVersion, c.AppVersion}
}

func envMap(kv map[string]string) func(string) string {
	return func(key string) string { return kv[key] }
}

// TestDeviceConfigDefaults pins the default identity: every device field empty
// when its variable is unset. Empty is the default because client_identity.py
// passes only the kwargs whose variable is non-empty, so unset fields fall
// through to the client library's own default rather than to a value chosen
// here.
func TestDeviceConfigDefaults(t *testing.T) {
	got := identityOf(deviceConfigFrom(envMap(nil)))
	if want := (identity{}); got != want {
		t.Fatalf("unset identity = %+v, want %+v", got, want)
	}
}

// TestDeviceConfigEnvOverrides checks each variable lands in the field the
// Python _DEVICE_ENV map pairs it with.
func TestDeviceConfigEnvOverrides(t *testing.T) {
	cases := []struct {
		key, value string
		want       identity
		name       string
	}{
		{EnvDeviceModel, "Telegram MCP", identity{model: "Telegram MCP"}, "DeviceModel"},
		{EnvSystemVersion, "1.0", identity{system: "1.0"}, "SystemVersion"},
		{EnvAppVersion, "1.0", identity{app: "1.0"}, "AppVersion"},
	}
	for _, tc := range cases {
		got := identityOf(deviceConfigFrom(envMap(map[string]string{tc.key: tc.value})))
		if got != tc.want {
			t.Fatalf("%s=%q produced %+v, want %+v", tc.key, tc.value, got, tc.want)
		}
	}
}

// TestDeviceConfigBlankIsUnset covers Python's `if value:` guard: a variable
// set to an empty or whitespace-only string is not a device name.
func TestDeviceConfigBlankIsUnset(t *testing.T) {
	env := map[string]string{
		EnvDeviceModel:   "",
		EnvSystemVersion: "   ",
		EnvAppVersion:    "\t\n",
	}
	got := identityOf(deviceConfigFrom(envMap(env)))
	if want := (identity{}); got != want {
		t.Fatalf("blank identity = %+v, want %+v", got, want)
	}
}

// TestDeviceConfigTrimsAndSetsAll checks a fully configured identity: values
// are trimmed and all three fields are populated at once.
func TestDeviceConfigTrimsAndSetsAll(t *testing.T) {
	env := map[string]string{
		EnvDeviceModel:   "  Telegram MCP  ",
		EnvSystemVersion: " 1.0 ",
		EnvAppVersion:    " 1.0 ",
	}
	want := identity{model: "Telegram MCP", system: "1.0", app: "1.0"}
	if got := identityOf(deviceConfigFrom(envMap(env))); got != want {
		t.Fatalf("trimmed identity = %+v, want %+v", got, want)
	}
}

// TestDeviceConfigLeavesLibraryFieldsEmpty checks the fields Python has no
// environment variable for stay empty, so gogram applies its own defaults
// instead of this port overriding them.
func TestDeviceConfigLeavesLibraryFieldsEmpty(t *testing.T) {
	env := map[string]string{
		EnvDeviceModel:   "Telegram MCP",
		EnvSystemVersion: "1.0",
		EnvAppVersion:    "1.0",
	}
	got := deviceConfigFrom(envMap(env))
	if got.LangCode != "" || got.SystemLangCode != "" || got.LangPack != "" || got.Params != nil {
		t.Fatalf("library-owned fields = %q/%q/%q/%v, want empty",
			got.LangCode, got.SystemLangCode, got.LangPack, got.Params)
	}
}

// TestDeviceConfigReadsProcessEnv exercises the exported DeviceConfig against
// the real environment.
func TestDeviceConfigReadsProcessEnv(t *testing.T) {
	t.Setenv(EnvDeviceModel, "Telegram MCP")
	t.Setenv(EnvSystemVersion, "1.0")
	t.Setenv(EnvAppVersion, "1.0")

	want := identity{model: "Telegram MCP", system: "1.0", app: "1.0"}
	if got := identityOf(DeviceConfig()); got != want {
		t.Fatalf("DeviceConfig = %+v, want %+v", got, want)
	}
}

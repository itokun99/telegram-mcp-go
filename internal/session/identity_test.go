package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityHashHex16KnownVector(t *testing.T) {
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	if got, want := IdentityHashHex16("abc"), "ba7816bf8f01cfea"; got != want {
		t.Fatalf("IdentityHashHex16(abc) = %q, want %q", got, want)
	}
}

func TestLockPathForKnownVector(t *testing.T) {
	dir := t.TempDir()
	got := LockPathFor(dir, "abc")
	want := filepath.Join(dir, "session-ba7816bf8f01cfea.lock")
	if got != want {
		t.Fatalf("LockPathFor(%q, abc) = %q, want %q", dir, got, want)
	}
}

func TestAuthKeySessionIdentityKnownVector(t *testing.T) {
	want := "string:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := AuthKeySessionIdentity([]byte("abc")); got != want {
		t.Fatalf("AuthKeySessionIdentity(abc) = %q, want %q", got, want)
	}
}

// TestFileSessionIdentityIsAbsolute mirrors singleton.session_identity, which
// keys on os.path.abspath: a relative session path must still lock the same
// file from every working directory.
func TestFileSessionIdentityIsAbsolute(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	got := FileSessionIdentity(filepath.Join("rel", "x.session"))
	want := "file:" + filepath.Join(cwd, "rel", "x.session")
	if got != want {
		t.Fatalf("FileSessionIdentity(relative) = %q, want %q", got, want)
	}
	if !filepath.IsAbs(strings.TrimPrefix(got, "file:")) {
		t.Fatalf("FileSessionIdentity(%q) is not absolute: %q", "rel/x.session", got)
	}

	abs := filepath.Join(cwd, "y.session")
	if got := FileSessionIdentity(abs); got != "file:"+abs {
		t.Fatalf("FileSessionIdentity(%q) = %q, want %q", abs, got, "file:"+abs)
	}
}

func TestStringSessionIdentity(t *testing.T) {
	if got, want := StringSessionIdentity("1BvEabc"), "string:1BvEabc"; got != want {
		t.Fatalf("StringSessionIdentity = %q, want %q", got, want)
	}
}

func TestAnonSessionIdentityDistinguishesPointers(t *testing.T) {
	a, b := new(int), new(int)
	if AnonSessionIdentity(a) != AnonSessionIdentity(a) {
		t.Fatal("AnonSessionIdentity is not stable for one pointer")
	}
	if AnonSessionIdentity(a) == AnonSessionIdentity(b) {
		t.Fatal("distinct pointers share an identity")
	}
}

func TestNormalizeUsername(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"   ":       "",
		"@Alice":    "alice",
		"  @ALICE ": "alice",
		"alice":     "alice",
	}
	for in, want := range cases {
		if got := NormalizeUsername(in); got != want {
			t.Fatalf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

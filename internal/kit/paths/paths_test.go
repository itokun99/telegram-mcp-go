package paths

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// quietGate builds a Gate whose diagnostics go nowhere, keeping tests
// hermetic (the fallback/deny paths log through slog).
func quietGate(s Settings) *Gate {
	s.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(s)
}

// realTempDir mirrors tmp_path.resolve(): on macOS t.TempDir() lives under
// /var, a symlink to /private/var, and the Python tests resolve it away.
func realTempDir(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return real
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func mustWrite(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// mustSymlink mirrors the Python _symlink helper: it skips the test where
// symlinks are unavailable (e.g. unprivileged Windows).
func mustSymlink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
}

// symlinkedLayout mirrors the Python _symlinked_layout: an allowed root with
// symlinks pointing out of it, plus an outside directory.
func symlinkedLayout(t *testing.T) (root, outside string) {
	t.Helper()
	base := realTempDir(t)
	root = mustMkdir(t, filepath.Join(base, "root"))
	outside = mustMkdir(t, filepath.Join(base, "outside"))
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")
	mustSymlink(t, filepath.Join(root, "secret_link.txt"), filepath.Join(outside, "secret.txt"))
	mustSymlink(t, filepath.Join(root, "outside_dir"), outside)
	mustSymlink(t, filepath.Join(root, "dangling.bin"), filepath.Join(outside, "created.bin"))
	return root, outside
}

func assertContractError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestReadableRelativePathResolvesInsideFirstServerRoot(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	target := mustWrite(t, filepath.Join(root, "document.txt"), "ok")

	gate := quietGate(Settings{ServerRoots: []string{root}})
	resolved, err := gate.ResolveReadable(context.Background(), nil, "send_file", "document.txt")
	if err != nil {
		t.Fatalf("ResolveReadable: %v", err)
	}
	if resolved != target {
		t.Fatalf("resolved = %q, want %q", resolved, target)
	}
}

func TestReadablePathRejectsTraversal(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})

	resolved, err := gate.ResolveReadable(context.Background(), nil, "send_file", "../etc/passwd")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	assertContractError(t, err, "Path traversal is not allowed.")
}

func TestReadablePathRejectsOutsideRoot(t *testing.T) {
	base := realTempDir(t)
	root := mustMkdir(t, filepath.Join(base, "root"))
	outside := mustMkdir(t, filepath.Join(base, "outside"))
	outsideFile := mustWrite(t, filepath.Join(outside, "outside.txt"), "no")

	gate := quietGate(Settings{ServerRoots: []string{root}})
	resolved, err := gate.ResolveReadable(context.Background(), nil, "send_file", outsideFile)
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	assertContractError(t, err, "Path is outside allowed roots.")
}

func TestReadablePathRejectsSymlinkEscape(t *testing.T) {
	for _, rawPath := range []string{"secret_link.txt", filepath.Join("outside_dir", "secret.txt")} {
		t.Run(rawPath, func(t *testing.T) {
			root, _ := symlinkedLayout(t)
			gate := quietGate(Settings{ServerRoots: []string{root}})

			for _, path := range []string{rawPath, filepath.Join(root, rawPath)} {
				resolved, err := gate.ResolveReadable(context.Background(), nil, "send_file", path)
				if resolved != "" {
					t.Fatalf("ResolveReadable(%q) resolved = %q, want empty", path, resolved)
				}
				assertContractError(t, err, "Path is outside allowed roots.")
			}
		})
	}
}

func TestWritablePathRejectsSymlinkEscape(t *testing.T) {
	rawPaths := []string{"dangling.bin", filepath.Join("outside_dir", "new.bin"), filepath.Join("outside_dir", "nested", "new.bin")}
	for _, rawPath := range rawPaths {
		t.Run(rawPath, func(t *testing.T) {
			root, outside := symlinkedLayout(t)
			gate := quietGate(Settings{ServerRoots: []string{root}})

			resolved, err := gate.ResolveWritable(context.Background(), nil, "download_media", rawPath, "ignored.bin")
			if resolved != "" {
				t.Fatalf("ResolveWritable(%q) resolved = %q, want empty", rawPath, resolved)
			}
			assertContractError(t, err, "Path is outside allowed roots.")

			// Rejection happens before any directory is created on the
			// target side.
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatalf("read outside dir: %v", err)
			}
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			if !reflect.DeepEqual(names, []string{"secret.txt"}) {
				t.Fatalf("outside dir entries = %v, want [secret.txt]", names)
			}
		})
	}
}

func TestSymlinksThatStayInsideRootResolveToTheirTarget(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	realDir := mustMkdir(t, filepath.Join(root, "real"))
	target := mustWrite(t, filepath.Join(realDir, "file.txt"), "ok")
	mustSymlink(t, filepath.Join(root, "linked"), realDir)

	gate := quietGate(Settings{ServerRoots: []string{root}})
	readable, err := gate.ResolveReadable(context.Background(), nil, "send_file", filepath.Join("linked", "file.txt"))
	if err != nil {
		t.Fatalf("ResolveReadable: %v", err)
	}
	if readable != target {
		t.Fatalf("readable = %q, want %q", readable, target)
	}

	writable, err := gate.ResolveWritable(context.Background(), nil, "download_media", filepath.Join("linked", "new.bin"), "ignored.bin")
	if err != nil {
		t.Fatalf("ResolveWritable: %v", err)
	}
	if want := filepath.Join(root, "real", "new.bin"); writable != want {
		t.Fatalf("writable = %q, want %q", writable, want)
	}
}

func TestClientRootsReplaceServerAllowlist(t *testing.T) {
	base := realTempDir(t)
	serverRoot := mustMkdir(t, filepath.Join(base, "server_root"))
	clientRoot := mustMkdir(t, filepath.Join(base, "client_root"))
	mustWrite(t, filepath.Join(serverRoot, "server.txt"), "server")
	clientFile := mustWrite(t, filepath.Join(clientRoot, "client.txt"), "client")

	gate := quietGate(Settings{ServerRoots: []string{serverRoot}})
	lister := RootsFunc(func(context.Context) ([]Root, error) {
		return []Root{{URI: fileURI(clientRoot)}}, nil
	})

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if status != StatusReady || !reflect.DeepEqual(roots, []string{clientRoot}) {
		t.Fatalf("EffectiveRoots = %v, %q; want [%s], ready", roots, status, clientRoot)
	}

	resolved, err := gate.ResolveReadable(context.Background(), lister, "send_file", "client.txt")
	if err != nil {
		t.Fatalf("ResolveReadable: %v", err)
	}
	if resolved != clientFile {
		t.Fatalf("resolved = %q, want %q", resolved, clientFile)
	}
}

func TestEmptyClientRootsDisableFileTools(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "server_root"))
	mustWrite(t, filepath.Join(root, "server.txt"), "server")

	gate := quietGate(Settings{ServerRoots: []string{root}})
	lister := &fakeLister{}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if len(roots) != 0 || status != StatusClientDenyAll {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, client_deny_all", roots, status)
	}

	resolved, err := gate.ResolveReadable(context.Background(), lister, "send_file", "server.txt")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	if err == nil || !strings.Contains(err.Error(), "empty MCP Roots list") || !strings.Contains(err.Error(), "deny-all") {
		t.Fatalf("error = %v, want the deny-all message", err)
	}
}

func TestUnexpectedRootsErrorDisablesFilePathTools(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "server_root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})
	lister := &fakeLister{err: errTransportFailure}

	roots, status := gate.EffectiveRoots(context.Background(), lister)
	if len(roots) != 0 || status != StatusError {
		t.Fatalf("EffectiveRoots = %v, %q; want empty, error", roots, status)
	}

	resolved, err := gate.ResolveReadable(context.Background(), lister, "send_file", "anything.txt")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want a disabled message", err)
	}
}

func TestWritableDefaultPathUsesDownloadsSubdir(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})

	resolved, err := gate.ResolveWritable(context.Background(), nil, "download_media", "", "example.bin")
	if err != nil {
		t.Fatalf("ResolveWritable: %v", err)
	}
	want := filepath.Join(root, DefaultDownloadSubdir, "example.bin")
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
	if _, err := os.Stat(filepath.Dir(resolved)); err != nil {
		t.Fatalf("parent dir was not created: %v", err)
	}
}

func TestWritableDefaultFilenameIsSanitized(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	gate := quietGate(Settings{ServerRoots: []string{root}})

	resolved, err := gate.ResolveWritable(context.Background(), nil, "download_media", "", filepath.Join("..", "evil.bin"))
	if err != nil {
		t.Fatalf("ResolveWritable: %v", err)
	}
	if want := filepath.Join(root, DefaultDownloadSubdir, "evil.bin"); resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}

func TestExtensionAllowlistIsEnforcedForSticker(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	filePath := mustWrite(t, filepath.Join(root, "sticker.txt"), "bad")

	gate := quietGate(Settings{ServerRoots: []string{root}})
	resolved, err := gate.ResolveReadable(context.Background(), nil, "send_sticker", filePath)
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	if err == nil || !strings.Contains(err.Error(), "extension is not allowed") {
		t.Fatalf("error = %v, want an extension-allowlist message", err)
	}
}

func TestFileToolsDisabledWithoutAnyRoots(t *testing.T) {
	gate := quietGate(Settings{})

	resolved, err := gate.ResolveReadable(context.Background(), nil, "send_file", "anything.txt")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want a disabled message", err)
	}
}

func TestPathHelperEdges(t *testing.T) {
	root := mustMkdir(t, filepath.Join(realTempDir(t), "root"))
	fileRoot := mustWrite(t, filepath.Join(root, "allowed.txt"), "ok")

	if got := dedupePaths([]string{root, root, fileRoot}); !reflect.DeepEqual(got, []string{root, fileRoot}) {
		t.Fatalf("dedupePaths = %v, want [%s %s]", got, root, fileRoot)
	}
	if got := containsForbiddenPathPatterns("   "); got != "Path must not be empty." {
		t.Fatalf("empty path check = %q", got)
	}
	if got := containsForbiddenPathPatterns("*.txt"); !strings.Contains(got, "wildcard") {
		t.Fatalf("wildcard check = %q", got)
	}
	if got := containsForbiddenPathPatterns("safe/name.txt"); got != "" {
		t.Fatalf("safe path check = %q, want no error", got)
	}
	if _, err := CoerceRootURIToPath("https://example.com/root"); err == nil || !strings.Contains(err.Error(), "Unsupported root URI scheme") {
		t.Fatalf("https root coercion = %v, want unsupported scheme", err)
	}
	if got, err := CoerceRootURIToPath(fileURI(root)); err != nil || got != root {
		t.Fatalf("CoerceRootURIToPath(%q) = %q, %v; want %q", fileURI(root), got, err, root)
	}
	if !pathIsWithinRoot(fileRoot, fileRoot) {
		t.Fatal("a file root must contain itself")
	}
	if pathIsWithinRoot(root, fileRoot) {
		t.Fatal("a file root must not contain its parent directory")
	}
	if got := firstResolutionRoot([]string{fileRoot}); got != root {
		t.Fatalf("firstResolutionRoot(file root) = %q, want %q", got, root)
	}
	if err := quietGate(Settings{}).ensureExtensionAllowed("send_sticker", filepath.Join(root, "bad.txt")); err == nil || !strings.HasPrefix(err.Error(), "File extension is not allowed") {
		t.Fatalf("sticker extension check = %v", err)
	}
	if err := quietGate(Settings{}).ensureExtensionAllowed("send_file", filepath.Join(root, "any.txt")); err != nil {
		t.Fatalf("send_file extension check = %v", err)
	}

	tooBig := mustWrite(t, filepath.Join(root, "big.bin"), "12345")
	tiny := quietGate(Settings{MaxFileBytes: map[string]int64{"tiny_tool": 4}})
	if err := tiny.ensureSizeWithinLimit("tiny_tool", tooBig); err == nil || !strings.HasPrefix(err.Error(), "File is too large") {
		t.Fatalf("size check = %v", err)
	}
	if err := tiny.ensureSizeWithinLimit("unknown_tool", tooBig); err != nil {
		t.Fatalf("unknown tool size check = %v", err)
	}
}

func TestMoreFileResolutionEdges(t *testing.T) {
	base := realTempDir(t)
	root := mustMkdir(t, filepath.Join(base, "root"))
	nested := mustMkdir(t, filepath.Join(root, "nested"))
	mustWrite(t, filepath.Join(nested, "file.txt"), "ok")
	gate := quietGate(Settings{ServerRoots: []string{root}})
	ctx := context.Background()

	resolved, err := gate.ResolveReadable(ctx, nil, "send_file", "missing.txt")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	assertContractError(t, err, "File not found: missing.txt")

	resolved, err = gate.ResolveReadable(ctx, nil, "send_file", "nested")
	if resolved != "" {
		t.Fatalf("resolved = %q, want empty", resolved)
	}
	if err == nil || !strings.Contains(err.Error(), "Path is not a file") {
		t.Fatalf("error = %v, want a not-a-file message", err)
	}

	outPath, err := gate.ResolveWritable(ctx, nil, "download_media", filepath.Join("nested", "out.bin"), "ignored.bin")
	if err != nil {
		t.Fatalf("ResolveWritable: %v", err)
	}
	if want := filepath.Join(root, "nested", "out.bin"); outPath != want {
		t.Fatalf("outPath = %q, want %q", outPath, want)
	}

	outPath, err = gate.ResolveWritable(ctx, nil, "download_media", "../outside.bin", "ignored.bin")
	if outPath != "" {
		t.Fatalf("outPath = %q, want empty", outPath)
	}
	assertContractError(t, err, "Path traversal is not allowed.")

	outPath, err = gate.ResolveWritable(ctx, nil, "download_media", filepath.Join(base, "outside.bin"), "ignored.bin")
	if outPath != "" {
		t.Fatalf("outPath = %q, want empty", outPath)
	}
	assertContractError(t, err, "Path is outside allowed roots.")
}

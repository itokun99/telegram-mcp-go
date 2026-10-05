package paths

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// ResolveReadable mirrors runtime._resolve_readable_file_path: it resolves a
// readable file path against the effective roots, enforcing in order the
// traversal/pattern check, symlink resolution, root containment, file-ness,
// readability, the per-tool extension allowlist, and the size limit. On
// failure the error message is the contract string the tool body returns.
func (g *Gate) ResolveReadable(ctx context.Context, lister RootsLister, toolName, rawPath string) (string, error) {
	roots, err := g.EnsureRoots(ctx, lister, toolName)
	if err != nil {
		return "", err
	}

	if patternErr := containsForbiddenPathPatterns(rawPath); patternErr != "" {
		return "", errors.New(patternErr)
	}

	candidate := filepath.Clean(strings.TrimSpace(rawPath))
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(firstResolutionRoot(roots), candidate)
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("File not found: %s", rawPath)
		}
		return "", err
	}

	if !pathIsWithinAnyRoot(resolved, roots) {
		return "", errors.New("Path is outside allowed roots.")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Path is not a file: %s", resolved)
	}
	if !isReadable(resolved) {
		return "", fmt.Errorf("File is not readable: %s", resolved)
	}
	if err := g.ensureExtensionAllowed(toolName, resolved); err != nil {
		return "", err
	}
	if err := g.ensureSizeWithinLimit(toolName, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// ResolveWritable mirrors runtime._resolve_writable_file_path: like the
// readable gate, with the parent directory also required to stay inside a
// root, the parent created when missing, and a writability check. An empty
// rawPath lands the file in the downloads subdir of the first root.
func (g *Gate) ResolveWritable(ctx context.Context, lister RootsLister, toolName, rawPath, defaultFilename string) (string, error) {
	roots, err := g.EnsureRoots(ctx, lister, toolName)
	if err != nil {
		return "", err
	}

	var candidate string
	if strings.TrimSpace(rawPath) != "" {
		if patternErr := containsForbiddenPathPatterns(rawPath); patternErr != "" {
			return "", errors.New(patternErr)
		}
		candidate = filepath.Clean(strings.TrimSpace(rawPath))
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(firstResolutionRoot(roots), candidate)
		}
	} else {
		// Path(default_filename).name: a default filename is a name, never
		// a path, so directory components are stripped before joining.
		safeName := filepath.Base(defaultFilename)
		candidate = filepath.Join(firstResolutionRoot(roots), g.downloadSubdir, safeName)
	}

	resolved, err := realpathLenient(candidate)
	if err != nil {
		return "", err
	}
	parent, err := realpathLenient(filepath.Dir(resolved))
	if err != nil {
		return "", err
	}
	if !pathIsWithinAnyRoot(resolved, roots) || !pathIsWithinAnyRoot(parent, roots) {
		return "", errors.New("Path is outside allowed roots.")
	}

	if err := g.ensureExtensionAllowed(toolName, resolved); err != nil {
		return "", err
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("Directory not writable: %s", parent)
	}
	if !isWritableDir(parent) {
		return "", fmt.Errorf("Directory not writable: %s", parent)
	}
	return resolved, nil
}

// containsForbiddenPathPatterns mirrors _contains_forbidden_path_patterns:
// the contract message for an empty path, a wildcard/shell pattern, or a
// ".." traversal component, and "" when the raw path passes.
func containsForbiddenPathPatterns(rawPath string) string {
	value := strings.TrimSpace(rawPath)
	if value == "" {
		return "Path must not be empty."
	}
	for _, token := range disallowedPathPatterns {
		if strings.Contains(value, token) {
			return "Path contains disallowed wildcard/shell patterns."
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(value), "/") {
		if part == ".." {
			return "Path traversal is not allowed."
		}
	}
	return ""
}

// firstResolutionRoot mirrors _first_resolution_root: the first root when it
// is an existing directory, else its parent (a file root still resolves
// relative paths against the directory containing it).
func firstResolutionRoot(roots []string) string {
	first := roots[0]
	if info, err := os.Stat(first); err == nil && info.IsDir() {
		return first
	}
	return filepath.Dir(first)
}

// pathIsWithinRoot mirrors _path_is_within_root: the root is resolved (it
// may itself be a symlink), a root that is a file matches only an exact
// path, and a directory root matches itself and anything below it.
func pathIsWithinRoot(candidate, root string) bool {
	resolvedRoot, err := realpathLenient(root)
	if err != nil {
		return false
	}
	if info, err := os.Stat(resolvedRoot); err == nil && !info.IsDir() {
		return candidate == resolvedRoot
	}
	rel, err := filepath.Rel(resolvedRoot, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathIsWithinAnyRoot(candidate string, roots []string) bool {
	for _, root := range roots {
		if pathIsWithinRoot(candidate, root) {
			return true
		}
	}
	return false
}

// maxSymlinkFollows bounds symlink chasing in realpathLenient, like the
// kernel's ELOOP limit; exceeding it fails closed.
const maxSymlinkFollows = 255

// realpathLenient resolves path like filepath.EvalSymlinks, except that a
// non-existent tail is kept instead of failing and symlinks pointing at
// non-existent targets are still followed. It mirrors Python's
// pathlib.Path.resolve(strict=False), which the writable gate and root
// normalisation rely on: a dangling symlink inside a root must resolve to
// its outside target (and then fail the containment check), never to the
// symlink itself.
func realpathLenient(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cur := filepath.Clean(abs)
	for depth := 0; depth <= maxSymlinkFollows; depth++ {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(cur)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			cur = filepath.Clean(target)
			continue
		}
		dir, base := filepath.Dir(cur), filepath.Base(cur)
		if dir == cur {
			return cur, nil
		}
		parent, err := realpathLenient(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(parent, base), nil
	}
	return "", fmt.Errorf("too many symbolic links resolving %s", path)
}

// ensureExtensionAllowed mirrors _ensure_extension_allowed.
func (g *Gate) ensureExtensionAllowed(toolName, candidate string) error {
	allowlist := g.extensionAllowlists[toolName]
	if len(allowlist) == 0 {
		return nil
	}
	suffix := strings.ToLower(pythonSuffix(candidate))
	for _, ext := range allowlist {
		if suffix == ext {
			return nil
		}
	}
	allowed := append([]string(nil), allowlist...)
	sort.Strings(allowed)
	return fmt.Errorf("File extension is not allowed for %s. Allowed: %s.", toolName, strings.Join(allowed, ", "))
}

// pythonSuffix returns the extension like pathlib.Path.suffix: the last
// dot-component of the name, except that a leading-dot name (".bashrc") and
// a trailing dot ("file.") have no suffix.
func pythonSuffix(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == base || ext == "." {
		return ""
	}
	return ext
}

// ensureSizeWithinLimit mirrors _ensure_size_within_limit.
func (g *Gate) ensureSizeWithinLimit(toolName, candidate string) error {
	maxBytes, ok := g.maxFileBytes[toolName]
	if !ok || maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return err
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("File is too large for %s: %d bytes (limit: %d bytes).", toolName, info.Size(), maxBytes)
	}
	return nil
}

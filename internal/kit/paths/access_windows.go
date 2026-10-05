//go:build windows

package paths

import "golang.org/x/sys/windows"

// isReadable probes the file for read access the way os.access(path, R_OK)
// does on POSIX: opening it is the only reliable check on Windows.
func isReadable(path string) bool {
	return windowsAccessible(path, windows.GENERIC_READ, windows.FILE_ATTRIBUTE_NORMAL)
}

// isWritableDir probes the directory for write access (os.access W_OK).
// FILE_FLAG_BACKUP_SEMANTICS is required to open a directory handle.
func isWritableDir(path string) bool {
	return windowsAccessible(path, windows.GENERIC_WRITE, windows.FILE_FLAG_BACKUP_SEMANTICS)
}

func windowsAccessible(path string, access, attrs uint32) bool {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(ptr, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}

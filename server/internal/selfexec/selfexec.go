// Package selfexec resolves the executable backing the current process.
package selfexec

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Resolve prefers the OS-reported executable path. Some launch environments
// can omit that metadata, so it falls back to argv[0] using normal executable
// lookup semantics instead of treating a bare command name as relative to the
// current directory.
func Resolve() (string, error) {
	return resolveWith(os.Executable, os.Args)
}

var (
	stableOnce sync.Once
	stablePath string
	stableErr  error
)

// ResolveStable is like Resolve but guarantees that the returned path points to
// a file that will remain available for the lifetime of the current process. If
// the current executable is a temporary binary created by `go run`, it is copied
// to a stable location in the user cache directory and that path is returned.
func ResolveStable() (string, error) {
	stableOnce.Do(func() {
		stablePath, stableErr = resolveStableWith(os.Executable, os.Args, os.UserCacheDir)
	})
	return stablePath, stableErr
}

func resolveStableWith(
	osExecutable func() (string, error),
	args []string,
	userCacheDir func() (string, error),
) (string, error) {
	exePath, err := resolveWith(osExecutable, args)
	if err != nil {
		return "", err
	}

	if !isGoRunTempBinary(exePath) {
		return exePath, nil
	}

	data, err := os.ReadFile(exePath)
	if err != nil {
		return "", fmt.Errorf("read transient executable %q: %w", exePath, err)
	}

	cacheDir, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("user cache dir: %w", err)
	}

	stableDir := filepath.Join(cacheDir, "multica", "selfexec")
	if err := os.MkdirAll(stableDir, 0o755); err != nil {
		return "", fmt.Errorf("create stable executable dir: %w", err)
	}

	hash := sha256.Sum256(data)
	name := fmt.Sprintf("multica-%x", hash[:8])
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	stablePath := filepath.Join(stableDir, name)

	if _, err := os.Stat(stablePath); err == nil {
		return stablePath, nil
	}

	tmpPath := stablePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o755); err != nil {
		return "", fmt.Errorf("write stable executable: %w", err)
	}
	if err := os.Rename(tmpPath, stablePath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename stable executable: %w", err)
	}

	return stablePath, nil
}

// isGoRunTempBinary reports whether path looks like a `go run` temporary binary.
// `go run` builds the binary under a directory named go-build<N>/b001/exe.
func isGoRunTempBinary(path string) bool {
	parts := strings.Split(filepath.Clean(path), string(os.PathSeparator))
	for i := 0; i < len(parts)-2; i++ {
		if strings.HasPrefix(parts[i], "go-build") && parts[i+1] == "b001" && parts[i+2] == "exe" {
			return true
		}
	}
	return false
}

func resolveWith(osExecutable func() (string, error), args []string) (string, error) {
	exePath, err := osExecutable()
	if err == nil {
		return exePath, nil
	}
	osExecutableErr := fmt.Errorf("os.Executable: %w", err)

	if len(args) == 0 || args[0] == "" {
		return "", errors.Join(osExecutableErr, errors.New("argv[0] is empty"))
	}

	candidate, fallbackErr := exec.LookPath(args[0])
	if fallbackErr == nil {
		candidate, fallbackErr = filepath.Abs(candidate)
	}
	if fallbackErr == nil {
		var info os.FileInfo
		info, fallbackErr = os.Stat(candidate)
		if fallbackErr == nil && !info.Mode().IsRegular() {
			fallbackErr = fmt.Errorf("%s is not a regular file", candidate)
		}
	}
	if fallbackErr != nil {
		return "", errors.Join(
			osExecutableErr,
			fmt.Errorf("resolve argv[0] %q: %w", args[0], fallbackErr),
		)
	}

	return candidate, nil
}

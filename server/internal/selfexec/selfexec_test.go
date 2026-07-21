package selfexec

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestResolveWithUsesOSExecutable(t *testing.T) {
	want := filepath.Join(t.TempDir(), "does-not-need-to-exist")

	got, err := resolveWith(func() (string, error) {
		return want, nil
	}, nil)
	if err != nil {
		t.Fatalf("resolveWith() error = %v", err)
	}
	if got != want {
		t.Fatalf("resolveWith() = %q, want %q", got, want)
	}
}

func TestResolveWithFallsBackToArgv0(t *testing.T) {
	executableErr := errors.New("cannot find executable path")
	failExecutable := func() (string, error) { return "", executableErr }

	t.Run("absolute path", func(t *testing.T) {
		want := writeTestExecutable(t, t.TempDir(), "multica")

		got, err := resolveWith(failExecutable, []string{want})
		if err != nil {
			t.Fatalf("resolveWith() error = %v", err)
		}
		if got != want {
			t.Fatalf("resolveWith() = %q, want %q", got, want)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		dir := t.TempDir()
		want := writeTestExecutable(t, dir, "multica")
		t.Chdir(dir)
		argv0 := "." + string(os.PathSeparator) + filepath.Base(want)

		got, err := resolveWith(failExecutable, []string{argv0})
		if err != nil {
			t.Fatalf("resolveWith() error = %v", err)
		}
		if got != want {
			t.Fatalf("resolveWith() = %q, want %q", got, want)
		}
	})

	t.Run("PATH command", func(t *testing.T) {
		dir := t.TempDir()
		want := writeTestExecutable(t, dir, "multica")
		t.Setenv("PATH", dir)

		got, err := resolveWith(failExecutable, []string{"multica"})
		if err != nil {
			t.Fatalf("resolveWith() error = %v", err)
		}
		if got != want {
			t.Fatalf("resolveWith() = %q, want %q", got, want)
		}
	})
}

func TestResolveWithRejectsInvalidFallback(t *testing.T) {
	executableErr := errors.New("cannot find executable path")
	failExecutable := func() (string, error) { return "", executableErr }

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing argv0", args: nil, want: "argv[0] is empty"},
		{name: "empty argv0", args: []string{""}, want: "argv[0] is empty"},
		{name: "missing executable", args: []string{"multica-does-not-exist"}, want: "multica-does-not-exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveWith(failExecutable, tt.args)
			if err == nil {
				t.Fatal("resolveWith() error = nil, want failure")
			}
			if !errors.Is(err, executableErr) {
				t.Fatalf("error = %q, want original os.Executable error", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}

	if runtime.GOOS != "windows" {
		t.Run("non-executable file", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "multica")
			if err := os.WriteFile(path, []byte("not executable"), 0o644); err != nil {
				t.Fatalf("write non-executable fixture: %v", err)
			}

			_, err := resolveWith(failExecutable, []string{path})
			if err == nil {
				t.Fatal("resolveWith() error = nil, want failure")
			}
			if !errors.Is(err, executableErr) {
				t.Fatalf("error = %q, want original os.Executable error", err)
			}
		})
	}
}

func writeTestExecutable(t *testing.T, dir, base string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		base += ".exe"
	}
	path := filepath.Join(dir, base)
	if err := os.WriteFile(path, []byte("test executable"), 0o755); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}
	return path
}

func TestResolveStableWithReturnsExistingPath(t *testing.T) {
	want := writeTestExecutable(t, t.TempDir(), "multica")

	got, err := resolveStableWith(
		func() (string, error) { return want, nil },
		nil,
		func() (string, error) { return t.TempDir(), nil },
	)
	if err != nil {
		t.Fatalf("resolveStableWith() error = %v", err)
	}
	if got != want {
		t.Fatalf("resolveStableWith() = %q, want %q", got, want)
	}
}

func TestResolveStableWithCopiesGoRunTempBinary(t *testing.T) {
	dir := t.TempDir()
	realBinary := writeTestExecutable(t, dir, "multica")
	data, err := os.ReadFile(realBinary)
	if err != nil {
		t.Fatalf("read real binary: %v", err)
	}

	tempPath := filepath.Join(dir, "go-build1234567890", "b001", "exe", "multica")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		t.Fatalf("mkdir temp path: %v", err)
	}
	if err := os.WriteFile(tempPath, data, 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}

	got, err := resolveStableWith(
		func() (string, error) { return tempPath, nil },
		nil,
		func() (string, error) { return cacheDir, nil },
	)
	if err != nil {
		t.Fatalf("resolveStableWith() error = %v", err)
	}
	if got == tempPath {
		t.Fatalf("resolveStableWith() returned the temp path")
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("stable path does not exist: %v", err)
	}
	gotData, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read stable binary: %v", err)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatalf("stable binary content differs from source")
	}
}

func TestResolveStableWithReturnsSameStablePathOnRepeatedCalls(t *testing.T) {
	dir := t.TempDir()
	realBinary := writeTestExecutable(t, dir, "multica")
	data, err := os.ReadFile(realBinary)
	if err != nil {
		t.Fatalf("read real binary: %v", err)
	}

	tempPath := filepath.Join(dir, "go-build1234567890", "b001", "exe", "multica")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		t.Fatalf("mkdir temp path: %v", err)
	}
	if err := os.WriteFile(tempPath, data, 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}

	got1, err := resolveStableWith(
		func() (string, error) { return tempPath, nil },
		nil,
		func() (string, error) { return cacheDir, nil },
	)
	if err != nil {
		t.Fatalf("first resolveStableWith() error = %v", err)
	}

	got2, err := resolveStableWith(
		func() (string, error) { return tempPath, nil },
		nil,
		func() (string, error) { return cacheDir, nil },
	)
	if err != nil {
		t.Fatalf("second resolveStableWith() error = %v", err)
	}
	if got1 != got2 {
		t.Fatalf("resolveStableWith() returned different paths: %q vs %q", got1, got2)
	}
}

func TestResolveStableWithFailsOnMissingTempBinary(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "go-build1234567890", "b001", "exe", "multica")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}

	_, err := resolveStableWith(
		func() (string, error) { return tempPath, nil },
		nil,
		func() (string, error) { return cacheDir, nil },
	)
	if err == nil {
		t.Fatal("resolveStableWith() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "read transient executable") {
		t.Fatalf("error = %q, want read transient executable", err)
	}
}

func TestResolveStableCachesAcrossCalls(t *testing.T) {
	origOnce := stableOnce
	origPath := stablePath
	origErr := stableErr
	t.Cleanup(func() {
		stableOnce = origOnce
		stablePath = origPath
		stableErr = origErr
	})
	stableOnce = sync.Once{}
	stablePath = ""
	stableErr = nil

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Library", "Caches"), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".cache"), 0o755); err != nil {
		t.Fatalf("mkdir fallback cache dir: %v", err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, ".cache"))

	realBinary := writeTestExecutable(t, dir, "multica")
	data, err := os.ReadFile(realBinary)
	if err != nil {
		t.Fatalf("read real binary: %v", err)
	}
	tempPath := filepath.Join(dir, "go-build1234567890", "b001", "exe", "multica")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		t.Fatalf("mkdir temp path: %v", err)
	}
	if err := os.WriteFile(tempPath, data, 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	got1, err := ResolveStable()
	if err != nil {
		t.Fatalf("ResolveStable() error = %v", err)
	}
	if got1 == tempPath {
		t.Fatalf("ResolveStable() returned the temp path")
	}

	if err := os.Remove(tempPath); err != nil {
		t.Fatalf("remove temp binary: %v", err)
	}
	got2, err := ResolveStable()
	if err != nil {
		t.Fatalf("ResolveStable() second call error = %v", err)
	}
	if got1 != got2 {
		t.Fatalf("ResolveStable() returned different paths: %q vs %q", got1, got2)
	}
}

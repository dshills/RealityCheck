package cache

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestKey_LengthPrefixed(t *testing.T) {
	if Key("ab", "c") == Key("a", "bc") {
		t.Error("different splits of the same bytes must give different keys")
	}
	k1, k2 := Key("a"), Key("a")
	if k1 != k2 || len(k1) != 64 {
		t.Error("keys must be stable 64-char hex digests")
	}
}

func TestStore_PutGetStatsClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "realitycheck")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %v; want 0700", info.Mode().Perm())
	}
	if _, ok := s.Get(Key("missing")); ok {
		t.Error("missing key should miss")
	}

	k1, k2 := Key("one"), Key("two")
	if err := s.Put(k1, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(k2, []byte(`{"b":22}`)); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(k1); !ok || string(got) != `{"a":1}` {
		t.Errorf("Get = %q, %v", got, ok)
	}
	if info, err := os.Stat(filepath.Join(dir, k1+".json")); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("entry mode = %v; want 0600", info.Mode().Perm())
	}

	// A file the store did not write must survive Clear and not count.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, size, err := s.Stats(); err != nil || n != 2 || size != int64(len(`{"a":1}`)+len(`{"b":22}`)) {
		t.Errorf("Stats = %d, %d, %v", n, size, err)
	}
	if n, err := s.Clear(); err != nil || n != 2 {
		t.Errorf("Clear = %d, %v", n, err)
	}
	if _, ok := s.Get(k1); ok {
		t.Error("cleared entry should miss")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Clear removed a file it did not write: %v", err)
	}
	// No temporary files are left behind by Put.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d files after Clear, want only notes.txt", len(entries))
	}
}

func TestDefaultDir_HonorsXDG(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CACHE_HOME", abs)
	if dir, err := DefaultDir(); err != nil || dir != filepath.Join(abs, "realitycheck") {
		t.Errorf("DefaultDir = %q, %v", dir, err)
	}
	// A relative value is ignored, per the XDG spec.
	t.Setenv("XDG_CACHE_HOME", "relative/cache")
	if dir, err := DefaultDir(); err != nil || !filepath.IsAbs(dir) {
		t.Errorf("relative XDG_CACHE_HOME: DefaultDir = %q, %v; want an absolute fallback", dir, err)
	}
}

func TestOpen_ExistingDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits")
	}
	for _, tc := range []struct {
		mode    os.FileMode
		wantErr bool
	}{
		{0o755, false}, // readable by others: tightened to 0700
		{0o775, true},  // group-writable: entries may be planted
		{0o777, true},
	} {
		dir := filepath.Join(t.TempDir(), "cache")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, tc.mode); err != nil {
			t.Fatal(err)
		}
		_, err := Open(dir)
		if (err != nil) != tc.wantErr {
			t.Errorf("mode %04o: err = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
		if tc.wantErr {
			continue
		}
		if info, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o700 {
			t.Errorf("mode %04o: not tightened to 0700 (%04o)", tc.mode, info.Mode().Perm())
		}
	}
}

func TestStore_RejectsBadKeysAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "target.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../target", "", "ABC", Key("x") + "/../../target"} {
		if _, ok := s.Get(key); ok {
			t.Errorf("Get(%q) should miss", key)
		}
		if err := s.Put(key, []byte("x")); err == nil {
			t.Errorf("Put(%q) should fail", key)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "{}" {
		t.Error("a bad key reached a file outside the store")
	}
	link := Key("linked")
	if err := os.Symlink(outside, filepath.Join(s.Dir(), link+".json")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need extra privileges on Windows")
		}
		t.Fatal(err)
	}
	if _, ok := s.Get(link); ok {
		t.Error("a symlinked entry should miss")
	}
}

func TestOpen_RejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("a regular file should not open as a cache directory")
	}
}

func TestClear_RemovesStaleTempFilesOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(s.Dir(), tempPrefix+"stale")
	fresh := filepath.Join(s.Dir(), tempPrefix+"fresh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTemp)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale temp file should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh temp file may belong to a write in progress and must stay")
	}
}

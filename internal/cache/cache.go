// Package cache stores analysis results on disk, keyed by a content hash of
// everything that determines them, so repeated runs over unchanged input
// skip the LLM call.
package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// tempPrefix names the temporary files Put writes before renaming them.
const tempPrefix = ".tmp-"

// Store is a directory of cache entries, one file per key.
type Store struct {
	dir string
}

// entryName matches the files Store writes: a hex SHA-256 key plus ".json".
// Clear and Stats touch nothing else in the directory.
var entryName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

// DefaultDir is $XDG_CACHE_HOME/realitycheck, or realitycheck under the
// platform's user cache directory when XDG_CACHE_HOME is unset or, as the
// XDG spec requires, not absolute.
func DefaultDir() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" || !filepath.IsAbs(base) {
		var err error
		if base, err = os.UserCacheDir(); err != nil {
			return "", fmt.Errorf("cache: locate user cache directory: %w", err)
		}
	}
	return filepath.Join(base, "realitycheck"), nil
}

// Open returns the store at dir, creating the directory if needed. Entries
// are trusted on read, so on Unix a directory that group or others can
// write is refused: entries planted there would survive any later chmod.
// One they can only read is restricted to 0700, since entries describe the
// code. Windows has no such mode bits (Go reports 0777 for ordinary
// directories); access there follows the per-user ACLs of the user cache
// directory, so the mode is not checked.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cache: create %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cache: %s is not a directory", dir)
	}
	if runtime.GOOS == "windows" {
		return &Store{dir: dir}, nil
	}
	perm := info.Mode().Perm()
	if perm&0o022 != 0 {
		return nil, fmt.Errorf("cache: %s is writable by other users (mode %04o); remove it or restrict it to 0700", dir, perm)
	}
	if perm&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("cache: %s is readable by other users and cannot be made private: %w", dir, err)
		}
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// Key hashes parts into a cache key. Each part is length-prefixed, so
// different splits of the same bytes give different keys.
func Key(parts ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// path returns the entry file for key, or false for anything but a key
// Key produces, so a key can never name a path outside the store.
func (s *Store) path(key string) (string, bool) {
	name := key + ".json"
	if !entryName.MatchString(name) {
		return "", false
	}
	return filepath.Join(s.dir, name), true
}

// Get returns the entry stored under key. A missing, unreadable, or
// non-regular entry (such as a symlink) is a miss.
func (s *Store) Get(key string) ([]byte, bool) {
	path, ok := s.path(key)
	if !ok {
		return nil, false
	}
	linfo, err := os.Lstat(path)
	if err != nil || !linfo.Mode().IsRegular() {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	// The file opened must be the one checked: an entry swapped for a
	// symlink in between is a miss.
	if finfo, err := f.Stat(); err != nil || !os.SameFile(linfo, finfo) {
		return nil, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores data under key. The write is atomic: concurrent runs never see
// a partial entry. Put can fail when another run writes or (on Windows)
// reads the same key at once; the cache is only an optimization, so
// callers should treat an error as a warning.
func (s *Store) Put(key string, data []byte) error {
	path, ok := s.path(key)
	if !ok {
		return fmt.Errorf("cache: invalid key %q", key)
	}
	tmp, err := os.CreateTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("cache: write: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cache: write: %w", err)
	}
	// Flush before the rename publishes the entry, so a crash cannot leave
	// a torn entry under a valid key.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cache: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: write: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cache: write: %w", err)
	}
	return nil
}

// Stats returns the number of entries and their total size in bytes.
func (s *Store) Stats() (entries int, size int64, err error) {
	err = s.eachEntry(func(path string, info fs.FileInfo) error {
		entries++
		size += info.Size()
		return nil
	})
	return entries, size, err
}

// staleTemp is how old a temporary file must be before Clear treats it as
// left behind by a run that died mid-write rather than one in progress.
const staleTemp = time.Hour

// Clear removes every entry, and temporary files left by interrupted
// writes, and returns how many entries were removed. Other files in the
// directory are left alone.
func (s *Store) Clear() (removed int, err error) {
	if dirents, err := os.ReadDir(s.dir); err == nil {
		for _, d := range dirents {
			if !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), tempPrefix) {
				continue
			}
			if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > staleTemp {
				_ = os.Remove(filepath.Join(s.dir, d.Name()))
			}
		}
	}
	err = s.eachEntry(func(path string, _ fs.FileInfo) error {
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed concurrently; not ours to count
			}
			return err
		}
		removed++
		return nil
	})
	return removed, err
}

func (s *Store) eachEntry(fn func(path string, info fs.FileInfo) error) error {
	dirents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cache: read %s: %w", s.dir, err)
	}
	for _, d := range dirents {
		if !d.Type().IsRegular() || !entryName.MatchString(d.Name()) {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		if err := fn(filepath.Join(s.dir, d.Name()), info); err != nil {
			return fmt.Errorf("cache: %w", err)
		}
	}
	return nil
}

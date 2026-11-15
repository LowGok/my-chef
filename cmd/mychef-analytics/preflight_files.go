package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/vfs"
)

// Input witnesses retain only digests and file metadata, never database bytes.
// Every SQLite file is opened O_RDONLY. A private, lockless VFS lets SQLite
// read its WAL with a heap index instead of creating or updating a shared SHM.
// Files must remain unchanged for the whole check; otherwise readiness fails.
type preflightInputs map[string]preflightWitness
type preflightWitness struct {
	info   os.FileInfo
	digest string
	absent bool
}

func privatePreflightPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return analytics.ErrPrivacy
	}
	for parent := path; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return analytics.ErrPrivacy
		}
		if parent == path && (info.IsDir() != directory || info.Mode().Perm()&0077 != 0 || !directory && !info.Mode().IsRegular()) {
			return analytics.ErrPrivacy
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func (inputs preflightInputs) read(path string, maximum int64) ([]byte, error) {
	if privatePreflightPath(path, false) != nil {
		return nil, analytics.ErrPrivacy
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, analytics.ErrPrivacy
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() > maximum {
		return nil, analytics.ErrInput
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, analytics.ErrInput
	}
	witness := preflightWitness{info: info, digest: analytics.Digest(raw)}
	if previous, exists := inputs[path]; exists && !samePreflightWitness(previous, witness) {
		return nil, analytics.ErrPrivacy
	}
	inputs[path] = witness
	return raw, nil
}

func (inputs preflightInputs) observe(path string, maximum int64) error {
	if privatePreflightPath(path, false) != nil {
		return analytics.ErrPrivacy
	}
	file, err := os.Open(path)
	if err != nil {
		return analytics.ErrPrivacy
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() > maximum {
		return analytics.ErrInput
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || n > maximum {
		return analytics.ErrInput
	}
	witness := preflightWitness{info: info, digest: hex.EncodeToString(hash.Sum(nil))}
	if previous, exists := inputs[path]; exists && !samePreflightWitness(previous, witness) {
		return analytics.ErrPrivacy
	}
	inputs[path] = witness
	return nil
}

func samePreflightWitness(a, b preflightWitness) bool {
	return a.absent == b.absent && (a.absent || a.digest == b.digest && os.SameFile(a.info, b.info) &&
		a.info.Mode() == b.info.Mode() && a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime()))
}

func (inputs preflightInputs) unchanged() bool {
	for path, before := range inputs {
		if before.absent {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return false
			}
			continue
		}
		other := preflightInputs{}
		if err := other.observe(path, max(before.info.Size(), 1)); err != nil || !samePreflightWitness(before, other[path]) {
			return false
		}
	}
	return true
}

type preflightVFS struct {
	paths map[string]bool
}

func (v preflightVFS) Open(path string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	if !v.paths[path] || flags&(vfs.OPEN_MAIN_DB|vfs.OPEN_WAL) == 0 || privatePreflightPath(path, false) != nil {
		return nil, 0, sqlite3.CANTOPEN
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, sqlite3.CANTOPEN
	}
	return preflightReadFile{file}, (flags &^ (vfs.OPEN_CREATE | vfs.OPEN_READWRITE)) | vfs.OPEN_READONLY, nil
}
func (v preflightVFS) Delete(string, bool) error { return sqlite3.READONLY }
func (v preflightVFS) Access(path string, _ vfs.AccessFlag) (bool, error) {
	if !v.paths[path] {
		return false, nil
	}
	_, err := os.Lstat(path)
	return err == nil, nil
}
func (v preflightVFS) FullPathname(path string) (string, error) {
	if !v.paths[path] {
		return "", sqlite3.CANTOPEN
	}
	return path, nil
}

type preflightReadFile struct{ *os.File }

func (f preflightReadFile) WriteAt([]byte, int64) (int, error) { return 0, sqlite3.READONLY }
func (f preflightReadFile) Truncate(int64) error               { return sqlite3.READONLY }
func (f preflightReadFile) Sync(vfs.SyncFlag) error            { return sqlite3.READONLY }
func (f preflightReadFile) Size() (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
func (f preflightReadFile) Lock(vfs.LockLevel) error                        { return nil }
func (f preflightReadFile) Unlock(vfs.LockLevel) error                      { return nil }
func (f preflightReadFile) CheckReservedLock() (bool, error)                { return false, nil }
func (f preflightReadFile) SectorSize() int                                 { return 4096 }
func (f preflightReadFile) DeviceCharacteristics() vfs.DeviceCharacteristic { return 0 }

type preflightReadDB struct {
	*sql.DB
	vfsName string
}

func (db *preflightReadDB) Close() error {
	err := db.DB.Close()
	vfs.Unregister(db.vfsName)
	return err
}

func (inputs preflightInputs) database(ctx context.Context, path string) (*preflightReadDB, error) {
	paths := map[string]bool{path: true, path + "-wal": true}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		p := path + suffix
		if _, err := os.Lstat(p); os.IsNotExist(err) && suffix != "" {
			if previous, exists := inputs[p]; exists && !previous.absent {
				return nil, analytics.ErrPrivacy
			}
			inputs[p] = preflightWitness{absent: true}
			continue
		}
		if err := inputs.observe(p, 512<<20); err != nil {
			return nil, err
		}
		if suffix == "-journal" {
			// A rollback journal needs recovery; preflight must not perform it.
			return nil, analytics.ErrPrivacy
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, analytics.ErrPrivacy
	}
	name := "mychef_preflight_" + hex.EncodeToString(nonce[:])
	vfs.Register(name, preflightVFS{paths: paths})
	u := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"mode": {"ro"}, "vfs": {name},
		"_pragma": {"locking_mode(EXCLUSIVE)", "query_only(1)", "temp_store(MEMORY)", "busy_timeout(1000)"}}.Encode()}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		vfs.Unregister(name)
		return nil, analytics.ErrPrivacy
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		vfs.Unregister(name)
		return nil, errors.Join(analytics.ErrPrivacy, err, db.Close())
	}
	return &preflightReadDB{DB: db, vfsName: name}, nil
}

func outsidePreflightRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

package imports

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

var ErrImportImage = errors.New("the recipe photos could not be prepared; choose up to three JPEG, PNG or HEIC images within the size limits")

// A minute of slack lets the periodic sweep enforce the one-hour maximum.
const importImageLifetime = 59 * time.Minute

type ImageLimits struct {
	MaxImages                                        int
	MaxInputBytes, MaxAggregateBytes, MaxStoredBytes int64
	MaxStoredFiles                                   int
}

func DefaultImageLimits() ImageLimits {
	return ImageLimits{MaxImages: 3, MaxInputBytes: 10 << 20, MaxAggregateBytes: 12 << 20, MaxStoredBytes: 64 << 20, MaxStoredFiles: 32}
}
func (l ImageLimits) valid() bool {
	return l.MaxImages >= 1 && l.MaxImages <= 3 && l.MaxInputBytes > 0 && l.MaxInputBytes <= 10<<20 && l.MaxAggregateBytes >= l.MaxInputBytes && l.MaxAggregateBytes <= 12<<20 && l.MaxStoredBytes >= l.MaxAggregateBytes && l.MaxStoredBytes <= 256<<20 && l.MaxStoredFiles >= l.MaxImages && l.MaxStoredFiles <= 128
}

// ImageSpool owns a dedicated, non-backup ephemeral directory. A lifetime lock
// prevents a second process from treating a running upload as abandoned.
type ImageSpool struct {
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
	limits ImageLimits
	closed bool
}

func (*ImageSpool) String() string   { return "private recipe-image spool" }
func (*ImageSpool) GoString() string { return "private recipe-image spool" }

func NewImageSpool(directory string, limits ImageLimits) (spool *ImageSpool, err error) {
	if !filepath.IsAbs(directory) || !limits.valid() {
		return nil, ErrImportImage
	}
	if e := os.Mkdir(directory, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return nil, ErrImportImage
	}
	info, e := os.Lstat(directory)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrImportImage
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return nil, ErrImportImage
	}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	lock, e := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, ErrImportImage
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = lock.Close()
		return nil, ErrImportImage
	}
	spool = &ImageSpool{root: root, lock: lock, limits: limits}
	if err = spool.sweep(time.Now().UTC(), true); err != nil {
		_ = lock.Close()
		return nil, ErrImportImage
	}
	return spool, nil
}
func (s *ImageSpool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	cleanup := s.sweep(time.Now().UTC(), true)
	a := s.lock.Close()
	b := s.root.Close()
	if cleanup != nil || a != nil || b != nil {
		return ErrImportImage
	}
	return nil
}

type ImageNormalizeFunc func(context.Context, []byte) ([]byte, error)
type StagedImages struct {
	spool   *ImageSpool
	names   []string
	expires time.Time
	removed bool
}

func (*StagedImages) String() string   { return "private staged recipe images" }
func (*StagedImages) GoString() string { return "private staged recipe images" }

type importImageReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r importImageReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.reader.Read(p)
}

func copyImportImage(ctx context.Context, destination io.Writer, reader io.Reader, maximum int64) (int64, error) {
	stop := func() bool { return true }
	if closer, ok := reader.(io.ReadCloser); ok {
		stop = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	defer stop()
	bounded := importImageReader{ctx: ctx, reader: reader}
	n, err := io.Copy(destination, io.LimitReader(bounded, maximum))
	if err != nil {
		return n, err
	}
	// Detect an oversized upload without putting even one excess byte on disk.
	var extra [1]byte
	got, err := io.ReadFull(bounded, extra[:])
	if got != 0 {
		return n, ErrImportImage
	}
	if err != nil && err != io.EOF {
		return n, err
	}
	return n, nil
}

// Stage never accepts a filename or a client-selected path. Normalization runs
// through a bounded decoding child in production; fakes are injected in tests.
func (s *ImageSpool) Stage(ctx context.Context, readers []io.Reader, normalize ImageNormalizeFunc, now time.Time) (*StagedImages, error) {
	if len(readers) < 1 || len(readers) > s.limits.MaxImages {
		return nil, ErrImportImage
	}
	index := 0
	return s.StageStream(ctx, func() (io.Reader, error) {
		if index == len(readers) {
			return nil, io.EOF
		}
		reader := readers[index]
		index++
		return reader, nil
	}, normalize, now)
}

// StageStream consumes multipart parts in order without ParseMultipartForm,
// filenames, default temporary files or a size allowance reset between parts.
func (s *ImageSpool) StageStream(ctx context.Context, next func() (io.Reader, error), normalize ImageNormalizeFunc, now time.Time) (batch *StagedImages, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || normalize == nil || now.IsZero() || next == nil || ctx.Err() != nil {
		return nil, ErrImportImage
	}
	batch = &StagedImages{spool: s, names: []string{}, expires: now.Add(importImageLifetime)}
	pending := []string{}
	defer func() {
		if err != nil {
			for _, name := range append(pending, batch.names...) {
				_ = s.root.Remove(name)
			}
			batch = nil
		}
	}()
	var total int64
	for count := 0; ; count++ {
		reader, nextErr := next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil || count >= s.limits.MaxImages {
			return batch, ErrImportImage
		}
		if reader == nil {
			return batch, ErrImportImage
		}
		// Reserve the largest possible next original before writing. Stored clean
		// derivatives also count against this independent on-disk ceiling.
		used, count, e := s.usage()
		if e != nil || count >= s.limits.MaxStoredFiles || used+s.limits.MaxInputBytes > s.limits.MaxStoredBytes {
			return batch, ErrImportImage
		}
		token := make([]byte, 32)
		if _, e = rand.Read(token); e != nil {
			return batch, ErrImportImage
		}
		name := hex.EncodeToString(token) + ".upload"
		file, e := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return batch, ErrImportImage
		}
		pending = append(pending, name)
		maximum := min(s.limits.MaxInputBytes, s.limits.MaxAggregateBytes-total)
		n, copyErr := copyImportImage(ctx, file, reader, maximum)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n == 0 || n > maximum || ctx.Err() != nil {
			return batch, ErrImportImage
		}
		total += n
		raw, e := s.root.ReadFile(name)
		if e != nil {
			return batch, ErrImportImage
		}
		clean, e := normalize(ctx, raw)
		clear(raw)
		if removeErr := s.root.Remove(name); removeErr != nil {
			return batch, ErrImportImage
		}
		pending = pending[:len(pending)-1]
		if e != nil || ctx.Err() != nil || !validCleanRecipeImage(clean) {
			clear(clean)
			return batch, ErrImportImage
		}
		cleanName := hex.EncodeToString(token) + ".jpg"
		// The derivative is at most 2 MiB, but it must also fit the spool ceiling.
		used, count, e = s.usage()
		if e != nil || count >= s.limits.MaxStoredFiles || used+int64(len(clean)) > s.limits.MaxStoredBytes {
			clear(clean)
			return batch, ErrImportImage
		}
		out, e := s.root.OpenFile(cleanName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			clear(clean)
			return batch, ErrImportImage
		}
		batch.names = append(batch.names, cleanName)
		_, writeErr := out.Write(clean)
		clear(clean)
		closeErr = out.Close()
		if writeErr != nil || closeErr != nil || s.root.Chtimes(cleanName, now, now) != nil {
			return batch, ErrImportImage
		}
	}
	if len(batch.names) == 0 {
		return batch, ErrImportImage
	}
	return batch, nil
}
func validCleanRecipeImage(data []byte) bool {
	if len(data) < 4 || len(data) > 2<<20 || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) || !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
		return false
	}
	c, e := jpeg.DecodeConfig(bytes.NewReader(data))
	if e != nil || c.Width < 1 || c.Height < 1 || c.Width > 2048 || c.Height > 2048 {
		return false
	}
	// Accept only the baseline JPEG our decoder child emits, including the
	// complete entropy stream. Metadata inserted after SOS is rejected too.
	pos, inScan := 2, false
	for pos < len(data) {
		if inScan {
			next := bytes.IndexByte(data[pos:], 0xff)
			if next < 0 {
				return false
			}
			pos += next
			if pos+1 >= len(data) {
				return false
			}
			marker := data[pos+1]
			pos += 2
			if marker == 0 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			return marker == 0xd9 && pos == len(data)
		}
		if pos+4 > len(data) || data[pos] != 0xff {
			return false
		}
		marker := data[pos+1]
		if marker != 0xc0 && marker != 0xc4 && marker != 0xdb && marker != 0xda {
			return false
		}
		size := int(data[pos+2])<<8 | int(data[pos+3])
		if size < 2 || size > len(data)-pos-2 {
			return false
		}
		pos += 2 + size
		if marker == 0xda {
			inScan = true
		}
	}
	return false
}

func (b *StagedImages) Read(ctx context.Context, now time.Time) ([][]byte, error) {
	s := b.spool
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || b.removed || !now.Before(b.expires) || ctx.Err() != nil {
		return nil, ErrImportImage
	}
	result := make([][]byte, 0, len(b.names))
	for _, name := range b.names {
		info, e := s.root.Lstat(name)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return nil, ErrImportImage
		}
		file, e := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return nil, ErrImportImage
		}
		data, readErr := io.ReadAll(io.LimitReader(importImageReader{ctx: ctx, reader: file}, 2<<20+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || !validCleanRecipeImage(data) {
			clear(data)
			return nil, ErrImportImage
		}
		result = append(result, data)
	}
	return result, nil
}
func (b *StagedImages) Close() error {
	s := b.spool
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.removed {
		return nil
	}
	b.removed = true
	if s.closed {
		return nil
	} // the next startup removes all abandoned files.
	for _, name := range b.names {
		if e := s.root.Remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
			return ErrImportImage
		}
	}
	return nil
}

var importImageName = regexp.MustCompile(`^[a-f0-9]{64}\.(upload|jpg)$`)

func (s *ImageSpool) usage() (int64, int, error) {
	dir, e := s.root.Open(".")
	if e != nil {
		return 0, 0, ErrImportImage
	}
	defer func() { _ = dir.Close() }()
	entries, e := dir.ReadDir(-1)
	if e != nil || len(entries) > s.limits.MaxStoredFiles+1 {
		return 0, 0, ErrImportImage
	}
	var size int64
	count := 0
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		info, e := entry.Info()
		if e != nil || !importImageName.MatchString(entry.Name()) || !info.Mode().IsRegular() {
			return 0, 0, ErrImportImage
		}
		size += info.Size()
		count++
	}
	return size, count, nil
}
func (s *ImageSpool) sweep(now time.Time, startup bool) error {
	dir, e := s.root.Open(".")
	if e != nil {
		return ErrImportImage
	}
	defer func() { _ = dir.Close() }()
	entries, e := dir.ReadDir(-1)
	if e != nil {
		return ErrImportImage
	}
	for _, entry := range entries {
		if !importImageName.MatchString(entry.Name()) {
			continue
		}
		info, e := s.root.Lstat(entry.Name())
		if e != nil {
			return ErrImportImage
		}
		if info.IsDir() {
			return ErrImportImage
		}
		if startup || info.Mode()&os.ModeSymlink != 0 || !info.ModTime().Add(importImageLifetime).After(now) || info.ModTime().After(now) {
			if e := s.root.Remove(entry.Name()); e != nil && !errors.Is(e, os.ErrNotExist) {
				return ErrImportImage
			}
		}
	}
	return nil
}
func (s *ImageSpool) Sweep(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrImportImage
	}
	return s.sweep(now, false)
}
func (s *ImageSpool) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if e := s.Sweep(now.UTC()); e != nil {
				return e
			}
		}
	}
}

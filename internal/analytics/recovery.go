package analytics

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
)

const maxRecoveryBytes = 256 << 20

type recoveryEnvelope struct {
	Version           int    `json:"version"`
	Binding           string `json:"binding"`
	Created, Deadline int64
	Nonce, Ciphertext []byte
}

func (s *Service) aead() (cipher.AEAD, error) {
	if len(s.backupKey) != 32 {
		return nil, ErrDisabled
	}
	block, e := aes.NewCipher(s.backupKey)
	if e != nil {
		return nil, ErrPrivacy
	}
	return cipher.NewGCM(block)
}
func writeFresh(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrPrivacy
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}
func (s *Service) Backup(ctx context.Context, source, destination string) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return ErrPrivacy
	}
	if e := s.maintainLocked(ctx); e != nil {
		return e
	}
	if e := s.registered(ctx, source); e != nil {
		return e
	}
	if e := s.confined(destination); e != nil {
		return e
	}
	aead, e := s.aead()
	if e != nil {
		return e
	}
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	sourceDataset, e := analyticsdataset.OpenProduction(ctx, source, s.binding, true)
	if e != nil {
		return e
	}
	subjects, e := sourceDataset.ProductionSubjects(ctx, false)
	e = errors.Join(e, sourceDataset.Close())
	if e != nil {
		return e
	}
	data, e := readBounded(source, maxRecoveryBytes)
	if e != nil {
		return e
	}
	defer clear(data)
	v := recoveryEnvelope{Version: 2, Binding: s.binding, Created: s.now().Unix(), Deadline: s.now().Unix() + 7*86400, Nonce: make([]byte, aead.NonceSize())}
	if _, e = rand.Read(v.Nonce); e != nil {
		return e
	}
	aad, _ := json.Marshal([]any{v.Version, v.Binding, v.Created, v.Deadline})
	v.Ciphertext = aead.Seal(nil, v.Nonce, data, aad)
	if _, e = s.privacy.ExecContext(ctx, "INSERT INTO copies(path,kind,reporting,generation,created,deadline,state) SELECT ?,'backup',reporting,?,?,?,'building' FROM copies WHERE path=? AND state='ready'", destination, a.Generation, v.Created, v.Deadline, source); e != nil {
		return e
	}
	for _, id := range subjects {
		if _, e = s.privacy.ExecContext(ctx, "INSERT INTO backup_subjects VALUES(?,?)", destination, id); e != nil {
			return e
		}
	}
	if e = s.writeAnchor(ctx); e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if e = writeFresh(destination, b); e != nil {
		return e
	}
	_, e = s.privacy.ExecContext(ctx, "UPDATE copies SET state='ready' WHERE path=?", destination)
	return e
}
func readBounded(path string, max int64) ([]byte, error) {
	if e := privatePath(path, false); e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrPrivacy
	}
	defer func() { _ = f.Close() }()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrPrivacy
	}
	return b, nil
}
func (s *Service) Restore(ctx context.Context, source, destination string) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return ErrPrivacy
	}
	if e := s.maintainLocked(ctx); e != nil {
		return e
	}
	if e := s.confined(source); e != nil {
		return e
	}
	if e := s.confined(destination); e != nil {
		return e
	}
	aead, e := s.aead()
	if e != nil {
		return e
	}
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	var created, deadline, reporting int64
	if e = s.privacy.QueryRowContext(ctx, "SELECT created,deadline,reporting FROM copies WHERE path=? AND kind='backup' AND state='ready'", source).Scan(&created, &deadline, &reporting); e != nil || s.now().Unix() >= deadline {
		return ErrPrivacy
	}
	b, e := readBounded(source, maxRecoveryBytes*2)
	if e != nil {
		return e
	}
	var v recoveryEnvelope
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&v) != nil || decoder.Decode(new(any)) != io.EOF || v.Version != 2 || v.Binding != s.binding || v.Created != created || v.Deadline != deadline || deadline > created+7*86400 || len(v.Nonce) != aead.NonceSize() {
		return ErrPrivacy
	}
	aad, _ := json.Marshal([]any{v.Version, v.Binding, v.Created, v.Deadline})
	data, e := aead.Open(nil, v.Nonce, v.Ciphertext, aad)
	if e != nil {
		return ErrPrivacy
	}
	defer clear(data)
	if _, e = s.privacy.ExecContext(ctx, "INSERT INTO copies(path,kind,reporting,generation,created,state) VALUES(?,'staging',?,?,?,'building')", destination, reporting, a.Generation, s.now().Unix()); e != nil {
		return e
	}
	if e = writeFresh(destination, data); e != nil {
		return e
	}
	d, e := analyticsdataset.OpenProduction(ctx, destination, s.binding, false)
	if e != nil {
		_ = s.destroyCopy(ctx, destination)
		return ErrPrivacy
	}
	if d.ProductionReportingUnix() != reporting {
		_ = d.Close()
		return ErrPrivacy
	}
	revoked, e := s.revokedSubjects(ctx)
	if e == nil {
		e = d.MaintainProduction(ctx, s.now().Unix(), a.Generation, revoked)
	}
	e = errors.Join(e, d.Close())
	if e != nil {
		_, _ = s.privacy.ExecContext(ctx, "UPDATE copies SET state='quarantined' WHERE path=?", destination)
		return ErrPrivacy
	}
	if _, e = s.privacy.ExecContext(ctx, "UPDATE copies SET kind='extract',state='ready',generation=? WHERE path=?", a.Generation, destination); e != nil {
		return e
	}
	return s.publishCurrent(ctx, destination)
}
func (s *Service) revokedSubjects(ctx context.Context) ([]string, error) {
	rows, e := s.privacy.QueryContext(ctx, "SELECT subject FROM links WHERE state='revoked'")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

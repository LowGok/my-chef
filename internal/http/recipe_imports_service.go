package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/store"
)

type RecipeImportWorker interface {
	Ready(context.Context) (imports.WorkerReady, error)
	CheckPage(context.Context, imports.WorkerRequest) (imports.WorkerResult, error)
	Extract(context.Context, imports.WorkerRequest) (imports.WorkerResult, error)
}
type RecipeImportService struct {
	store         *store.Store
	journal       *store.LifecycleJournal
	reader        store.PersonalEntitlementReader
	vault         *imports.DraftVault
	worker        RecipeImportWorker
	spool         *imports.ImageSpool
	normalize     imports.ImageNormalizeFunc
	allowFixture  bool
	root          context.Context
	stop          context.CancelFunc
	mu            sync.Mutex
	active        map[string]context.CancelFunc
	closed        bool
	work          sync.WaitGroup
	cleanupFailed atomic.Bool
}

func NewRecipeImportService(ctx context.Context, s *store.Store, j *store.LifecycleJournal, reader store.PersonalEntitlementReader, vault *imports.DraftVault, worker RecipeImportWorker, spool *imports.ImageSpool, normalize imports.ImageNormalizeFunc, allowFixture bool) (*RecipeImportService, error) {
	if s == nil || j == nil || vault == nil || worker == nil || spool == nil {
		return nil, store.ErrRecipeImportStorage
	}
	if err := s.SweepRecipeImports(ctx, time.Now().UTC(), true); err != nil {
		return nil, err
	}
	root, stop := context.WithCancel(ctx)
	return &RecipeImportService{store: s, journal: j, reader: reader, vault: vault, worker: worker, spool: spool, normalize: normalize, allowFixture: allowFixture, root: root, stop: stop, active: map[string]context.CancelFunc{}}, nil
}
func (s *RecipeImportService) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.stop()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.work.Wait()
}
func (s *RecipeImportService) capability(ctx context.Context, actor store.PersonalActor) error {
	if s == nil || s.cleanupFailed.Load() {
		return imports.ErrExtractionUnavailable
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return imports.ErrExtractionUnavailable
	}
	allowed, err := s.store.PersonalCapability(ctx, actor, s.reader, time.Now().UTC())
	if err != nil {
		return err
	}
	if !allowed {
		return store.ErrPersonalPermission
	}
	return nil
}
func (s *RecipeImportService) ready(ctx context.Context, ai bool) (imports.WorkerReady, error) {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := s.worker.Ready(probe)
	if err != nil || !status.Ready || status.Fixture && !s.allowFixture || ai && !status.AIAvailable {
		return status, imports.ErrExtractionUnavailable
	}
	return status, nil
}
func (s *RecipeImportService) startURL(ctx context.Context, actor store.PersonalActor, key, address string) (store.RecipeImport, error) {
	if err := s.capability(ctx, actor); err != nil {
		return store.RecipeImport{}, err
	}
	input := store.RecipeImportStart{RequestKey: key, Source: imports.ImportSource{Kind: "url", URL: address}}
	if op, err := s.store.LookupRecipeImport(ctx, actor, s.reader, s.vault, input, time.Now().UTC()); !errors.Is(err, store.ErrRecipeImportNotFound) {
		return op, err
	}
	status, err := s.ready(ctx, false)
	if err != nil {
		return store.RecipeImport{}, err
	}
	tracked, release, err := s.journal.TrackAccountWork(s.root, actor.UserID)
	if err != nil {
		return store.RecipeImport{}, err
	}
	op, lease, err := s.store.BeginRecipeImport(ctx, actor, s.reader, s.vault, input, time.Now().UTC())
	if err != nil || !lease.Dispatch {
		release()
		return op, err
	}
	s.launch(tracked, release, actor, op, lease, status, false, nil, nil)
	return op, nil
}
func (s *RecipeImportService) startPhotos(ctx context.Context, actor store.PersonalActor, key string, next func() (io.Reader, error)) (store.RecipeImport, error) {
	if err := s.capability(ctx, actor); err != nil {
		return store.RecipeImport{}, err
	}
	if s.normalize == nil {
		return store.RecipeImport{}, imports.ErrImportImage
	}
	tracked, release, err := s.journal.TrackAccountWork(s.root, actor.UserID)
	if err != nil {
		return store.RecipeImport{}, err
	}
	upload, cancelUpload := context.WithCancel(tracked)
	stopRequest := context.AfterFunc(ctx, cancelUpload)
	batch, err := s.spool.StageStream(upload, next, s.normalize, time.Now().UTC())
	stopRequest()
	cancelUpload()
	if err != nil || ctx.Err() != nil {
		release()
		if batch != nil {
			_ = batch.Close()
		}
		return store.RecipeImport{}, imports.ErrImportImage
	}
	images, err := batch.Read(ctx, time.Now().UTC())
	if err != nil {
		release()
		_ = batch.Close()
		return store.RecipeImport{}, err
	}
	digests := make([]string, len(images))
	for i, data := range images {
		sum := sha256.Sum256(data)
		digests[i] = hex.EncodeToString(sum[:])
	}
	input := store.RecipeImportStart{RequestKey: key, Source: imports.ImportSource{Kind: "photos"}, ImageDigests: digests}
	op, err := s.store.LookupRecipeImport(ctx, actor, s.reader, s.vault, input, time.Now().UTC())
	var status imports.WorkerReady
	var lease store.RecipeImportLease
	if errors.Is(err, store.ErrRecipeImportNotFound) {
		status, err = s.ready(ctx, true)
		if err == nil {
			op, lease, err = s.store.BeginRecipeImport(ctx, actor, s.reader, s.vault, input, time.Now().UTC())
		}
	}
	if err != nil || !lease.Dispatch {
		for _, data := range images {
			clear(data)
		}
		_ = batch.Close()
		release()
		return op, err
	}
	s.launch(tracked, release, actor, op, lease, status, true, images, batch)
	return op, nil
}
func (s *RecipeImportService) beginAI(ctx context.Context, actor store.PersonalActor, id string) (store.RecipeImport, error) {
	if err := s.capability(ctx, actor); err != nil {
		return store.RecipeImport{}, err
	}
	status, err := s.ready(ctx, true)
	if err != nil {
		return store.RecipeImport{}, err
	}
	tracked, release, err := s.journal.TrackAccountWork(s.root, actor.UserID)
	if err != nil {
		return store.RecipeImport{}, err
	}
	op, lease, err := s.store.BeginRecipeImportAI(ctx, actor, s.reader, s.vault, id, time.Now().UTC())
	if err != nil {
		release()
		return op, err
	}
	s.launch(tracked, release, actor, op, lease, status, true, nil, nil)
	return op, nil
}
func (s *RecipeImportService) launch(tracked context.Context, release func(), actor store.PersonalActor, op store.RecipeImport, lease store.RecipeImportLease, status imports.WorkerReady, extract bool, images [][]byte, batch *imports.StagedImages) {
	call, cancel := context.WithTimeout(tracked, 60*time.Second)
	cleanup := func() {
		cancel()
		release()
		for _, data := range images {
			clear(data)
		}
		if batch != nil {
			_ = batch.Close()
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.store.AbandonRecipeImport(finish, actor.UserID, op.OwnerInstance, lease, time.Now().UTC())
		stop()
		cleanup()
		return
	}
	s.active[op.ID] = cancel
	s.work.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.work.Done()
		defer cleanup()
		defer func() { s.mu.Lock(); delete(s.active, op.ID); s.mu.Unlock() }()
		finished := false
		defer func() {
			if !finished {
				finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = s.store.AbandonRecipeImport(finish, actor.UserID, op.OwnerInstance, lease, time.Now().UTC())
			}
		}()
		current, err := s.store.CheckRecipeImportDispatch(call, actor, s.reader, s.vault, lease, extract, status.Fixture, time.Now().UTC())
		if err != nil || call.Err() != nil {
			return
		}
		request := imports.WorkerRequest{OperationID: current.ID, AccountID: current.OwnerInstance, Source: current.Private.Source, Images: images}
		var result imports.WorkerResult
		if extract {
			if current.Private.Source.Kind == "url" {
				if current.Private.Inspection == nil {
					return
				}
				request.Text = current.Private.Inspection.FallbackText
			}
			result, err = s.worker.Extract(call, request)
		} else {
			result, err = s.worker.CheckPage(call, request)
		}
		if err != nil || call.Err() != nil {
			return
		}
		if _, err = s.store.CompleteRecipeImport(call, actor, s.reader, s.vault, lease, result.Private, time.Now().UTC()); err == nil {
			finished = true
		}
	}()
}
func (s *RecipeImportService) cancel(ctx context.Context, actor store.PersonalActor, id string) error {
	if err := s.store.CancelRecipeImport(ctx, actor, id, time.Now().UTC()); err != nil {
		return err
	}
	s.mu.Lock()
	if cancel := s.active[id]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	return nil
}
func (s *RecipeImportService) RunSweep(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if s.store.SweepRecipeImports(ctx, now.UTC(), false) != nil || s.spool.Sweep(now.UTC()) != nil {
				s.cleanupFailed.Store(true)
				return imports.ErrExtractionUnavailable
			}
		}
	}
}

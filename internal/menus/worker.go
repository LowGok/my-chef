package menus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type Options struct {
	PersonalEntitlements store.PersonalEntitlementReader
	AssetProvider        string
	Lifecycle            *store.LifecycleJournal
	TraceDir             string
	Shuffle              func([]string)
	Workers              int
	QueueLimit           int
	Logger               *slog.Logger
	Model                string
	Timeout              time.Duration
}
type imageTask struct {
	Owner   int64
	ID      int64
	Content domain.RecipeContent
}
type Service struct {
	store      *store.Store
	provider   providers.Generator
	images     providers.Images
	options    Options
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
	queue      chan int64
	imageQueue chan imageTask
	wg         sync.WaitGroup
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)

func New(db *store.Store, p providers.Generator, images providers.Images, options Options) (*Service, error) {
	if p == nil {
		return nil, errors.New("a recipe provider is required")
	}
	if options.Workers == 0 {
		options.Workers = 2
	}
	if options.QueueLimit == 0 {
		options.QueueLimit = 16
	}
	if options.Timeout == 0 {
		options.Timeout = 120 * time.Second
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Workers < 1 || options.Workers > 8 || options.QueueLimit < 1 || options.QueueLimit > 64 {
		return nil, errors.New("invalid worker bounds")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: db, provider: p, images: images, options: options, ctx: ctx, cancel: cancel, queue: make(chan int64, options.QueueLimit), imageQueue: make(chan imageTask, 64)}
	recoverCtx, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	err := db.DB.WithContext(recoverCtx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.Generation{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{"status": "failed", "stage": "failed", "error_code": "interrupted", "error_message": providers.PublicMessage("interrupted")}).Error; err != nil {
			return err
		}
		return tx.Model(&store.Meal{}).Where("image_status = ?", "pending").Update("image_status", "unavailable").Error
	})
	if err != nil {
		cancel()
		return nil, err
	}
	for range options.Workers {
		s.wg.Go(s.worker)
	}
	for range 2 {
		s.wg.Go(s.imageWorker)
	}
	return s, nil
}
func (s *Service) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) Status(ctx context.Context, user, id int64) (domain.Operation, error) {
	var row store.Generation
	err := s.store.DB.WithContext(ctx).Where("id = ? AND user_id = ?", id, user).First(&row).Error
	return row.Operation(), err
}

func (s *Service) Start(ctx context.Context, user int64, key string, input domain.MenuInput) (domain.Operation, error) {
	return s.start(ctx, user, key, input, store.PersonalActor{})
}

func (s *Service) StartPersonal(ctx context.Context, actor store.PersonalActor, key string, input domain.MenuInput) (domain.Operation, error) {
	return s.start(ctx, actor.UserID, key, input, actor)
}

func (s *Service) start(ctx context.Context, user int64, key string, input domain.MenuInput, actor store.PersonalActor) (domain.Operation, error) {
	if input.Validate() != nil || !keyPattern.MatchString(key) {
		return domain.Operation{}, providers.Failure("invalid_input")
	}
	payload, _ := json.Marshal(input)
	hash := sha256.Sum256(payload)
	requestHash := hex.EncodeToString(hash[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return domain.Operation{}, providers.Failure("rate_limited")
	}
	var existing store.Generation
	err := s.store.DB.WithContext(ctx).Where("user_id = ? AND request_key = ?", user, key).First(&existing).Error
	if err == nil {
		if existing.RequestHash != requestHash {
			return domain.Operation{}, providers.Failure("conflict")
		}
		return existing.Operation(), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Operation{}, err
	}
	if len(s.queue) >= cap(s.queue) {
		return domain.Operation{}, providers.Failure("rate_limited")
	}
	row := store.Generation{UserID: user, Key: key, RequestHash: requestHash, Status: "queued", Stage: "queued", RequestJSON: "{}", CreatedUnix: time.Now().UTC().Unix()}
	err = s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&store.Generation{}).Where("user_id = ? AND status IN ?", user, []string{"queued", "running"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return providers.Failure("conflict")
		}
		if err := store.CheckMenuReplacement(tx, user, input); err != nil {
			if errors.Is(err, domain.ErrMenuExists) {
				return providers.Failure("menu_exists")
			}
			return err
		}
		shuffle := s.options.Shuffle
		if shuffle == nil {
			shuffle = func(d []string) { rand.Shuffle(len(d), func(i, j int) { d[i], d[j] = d[j], d[i] }) }
		}
		favorites, err := store.CaptureFavorites(tx, user, input, shuffle, store.PersonalPlanning{Actor: actor, Entitlements: s.options.PersonalEntitlements, Now: time.Now()})
		if err != nil {
			return err
		}
		var owner store.User
		if err := tx.First(&owner, user).Error; err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		snapshot := domain.GenerationRequest{MenuInput: input, Preferences: owner.Preferences(), CurrentDate: time.Now().UTC().Format(time.DateOnly), OperationID: strconv.FormatInt(row.ID, 10), Favorites: favorites}
		b, e := json.Marshal(snapshot)
		if e != nil {
			return e
		}
		return tx.Model(&row).Update("request_json", string(b)).Error
	})
	if err != nil {
		return domain.Operation{}, err
	}
	select {
	case s.queue <- row.ID:
		return row.Operation(), nil
	case <-s.ctx.Done():
		s.failed(row.ID, "interrupted")
		return domain.Operation{}, providers.Failure("rate_limited")
	}
}

func (s *Service) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.queue:
			s.process(id)
		}
	}
}
func (s *Service) process(id int64) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			s.failed(id, "internal")
			s.options.Logger.Error("generation worker failed", "operationId", id)
		}
	}()
	ctx, cancel := context.WithTimeout(s.ctx, s.options.Timeout)
	defer cancel()
	var row store.Generation
	if err := s.store.DB.WithContext(ctx).First(&row, id).Error; err != nil {
		return
	}
	claimed := s.store.DB.WithContext(ctx).Model(&store.Generation{}).Where("id = ? AND status = ?", id, "queued").Updates(map[string]any{"status": "running", "stage": "generating"})
	if claimed.Error != nil || claimed.RowsAffected != 1 {
		if claimed.Error != nil {
			s.failed(id, "storage_unavailable")
		}
		return
	}
	var request domain.GenerationRequest
	if json.Unmarshal([]byte(row.RequestJSON), &request) != nil {
		s.failed(id, "provider_invalid")
		return
	}
	if s.options.Lifecycle != nil {
		tracked, release, err := s.options.Lifecycle.TrackAccountWork(ctx, row.UserID)
		if err != nil {
			return
		}
		defer release()
		ctx = tracked
		if s.options.TraceDir != "" {
			ctx = providers.WithTraceGuard(ctx, func(path string, write func() error) error {
				return s.options.Lifecycle.GuardAccountTrace(ctx, row.UserID, s.options.TraceDir, path, write)
			})
		}
	}
	s.options.Logger.Info("generation stage", "operationId", id, "stage", "generating", "model", s.options.Model)
	providerRequest := request
	providerRequest.Favorites = nil
	providerRequest.FavoriteRevisions = nil
	providerRequest.Dates = []string{}
	captured := map[string]bool{}
	for _, f := range request.Favorites {
		captured[f.Date] = true
	}
	for _, date := range request.Dates {
		if !captured[date] {
			providerRequest.Dates = append(providerRequest.Dates, date)
		}
	}
	var recipes []domain.RecipeContent
	var err error
	if len(providerRequest.Dates) > 0 {
		recipes, err = s.provider.Generate(ctx, providerRequest)
	}
	if err != nil {
		code := "provider_unavailable"
		var providerError *providers.Error
		if errors.As(err, &providerError) {
			code = providerError.Kind
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "provider_timeout"
		}
		if s.ctx.Err() != nil {
			code = "interrupted"
		}
		s.failed(id, code)
		return
	}
	if len(recipes) != len(providerRequest.Dates) {
		s.failed(id, "provider_invalid")
		return
	}
	for _, r := range recipes {
		if r.Validate(request.MenuInput, request.Preferences) != nil {
			s.failed(id, "provider_invalid")
			return
		}
	}
	if err = s.store.DB.WithContext(ctx).Model(&store.Generation{}).Where("id = ? AND user_id = ? AND status = ?", id, row.UserID, "running").Update("stage", "saving").Error; err != nil {
		s.failed(id, "storage_unavailable")
		return
	}
	menu, err := s.store.SaveMenu(ctx, row.UserID, id, request.MenuInput, recipes)
	if err != nil {
		if errors.Is(err, domain.ErrMenuExists) {
			s.failed(id, "menu_exists")
		} else {
			s.failed(id, "storage_unavailable")
		}
		return
	}
	s.options.Logger.Info("generation stage", "operationId", id, "stage", "finished", "recipes", len(recipes), "durationMs", time.Since(start).Milliseconds())
	generatedIndex := 0
	for _, r := range menu.Recipes {
		if captured[r.Date] {
			continue
		}
		content := recipes[generatedIndex]
		generatedIndex++
		recipeID, e := strconv.ParseInt(r.ID, 10, 64)
		if e != nil {
			continue
		}
		task := imageTask{Owner: row.UserID, ID: recipeID, Content: content}
		if s.images == nil {
			s.saveImage(task, providers.ImageResult{}, errors.New("image provider unavailable"))
			continue
		}
		select {
		case s.imageQueue <- task:
		default:
			s.saveImage(task, providers.ImageResult{}, errors.New("image queue full"))
		}
	}
}
func (s *Service) failed(id int64, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.store.DB.WithContext(ctx).Model(&store.Generation{}).Where("id = ? AND status IN ?", id, []string{"queued", "running"}).Updates(map[string]any{"status": "failed", "stage": "failed", "error_code": code, "error_message": providers.PublicMessage(code)}).Error
	s.options.Logger.Info("generation stage", "operationId", id, "stage", "failed", "code", code)
	if err != nil {
		s.options.Logger.Error("could not record generation outcome", "operationId", id)
	}
}
func (s *Service) imageWorker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case task := <-s.imageQueue:
			s.processImage(task)
		}
	}
}
func (s *Service) processImage(task imageTask) {
	defer func() {
		if recover() != nil {
			s.saveImage(task, providers.ImageResult{}, errors.New("image worker failed"))
		}
	}()
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()
	var intent *store.LifecycleWork
	if s.options.Lifecycle != nil {
		work, err := s.options.Lifecycle.ReserveRecipeImage(ctx, task.Owner, task.ID, assetProviderBinding(s.options.AssetProvider))
		if err != nil {
			s.saveImage(task, providers.ImageResult{}, err)
			return
		}
		intent = &work
		tracked, release, err := s.options.Lifecycle.TrackAccountWork(ctx, task.Owner)
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.options.Lifecycle.FinishImageUpload(cleanup, work.ID, false)
			s.saveImage(task, providers.ImageResult{}, err)
			return
		}
		defer release()
		ctx = providers.WithUploadIntent(tracked, work.Target, func(begin context.Context) error {
			return s.options.Lifecycle.BeginImageUpload(begin, work.ID, work.Target)
		})
	}
	result, err := s.images.Image(ctx, task.Content)
	if intent != nil {
		confirmed := err == nil && result.PublicID == intent.Target
		persist, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		saveErr := s.options.Lifecycle.FinishImageUpload(persist, intent.ID, confirmed)
		cancel()
		if err == nil && (!confirmed || saveErr != nil) {
			err = providers.Failure("provider_invalid")
			result = providers.ImageResult{}
		}
	}
	s.saveImage(task, result, err)
}
func (s *Service) saveImage(task imageTask, result providers.ImageResult, imageError error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	updates := map[string]any{"image_status": "unavailable"}
	u, err := url.Parse(result.URL)
	if imageError == nil && err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && result.PublicID != "" {
		updates = map[string]any{"image_status": "ready", "recipeimg": result.URL, "cloudinary_public_id": result.PublicID}
	}
	code := ""
	var failure *providers.Error
	if errors.As(imageError, &failure) {
		code = failure.Kind
	} else if imageError != nil {
		code = "image_unavailable"
	}
	s.options.Logger.Info("recipe image stage", "mealId", task.ID, "stage", updates["image_status"], "code", code)
	if err = s.store.DB.WithContext(ctx).Model(&store.Meal{}).Where("meal_id = ? AND creator_id = ? AND image_status = ?", task.ID, task.Owner, "pending").Updates(updates).Error; err != nil {
		s.options.Logger.Error("could not record image outcome", "mealId", task.ID)
	}
}

func assetProviderBinding(value string) string {
	if value == "" {
		return "fixture"
	}
	return value
}

package menus

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type lifecycleImageProvider struct {
	started chan string
	release chan struct{}
	lost    bool
	wrong   bool
}

func (p *lifecycleImageProvider) Image(ctx context.Context, _ domain.RecipeContent) (providers.ImageResult, error) {
	target := providers.UploadTarget(ctx)
	if target == "" {
		return providers.ImageResult{}, providers.Failure("missing_intent")
	}
	if err := providers.StartReservedUpload(ctx); err != nil {
		return providers.ImageResult{}, err
	}
	p.started <- target
	if p.release != nil {
		<-p.release
	} // A remote acceptance may complete after cancellation.
	if p.lost {
		return providers.ImageResult{}, providers.Failure("provider_timeout")
	}
	returned := target
	if p.wrong {
		returned = "unrelated-provider-object"
	}
	return providers.ImageResult{URL: "https://res.cloudinary.com/test/image/upload/" + returned + ".png", PublicID: returned}, nil
}
func TestMealImageWorkerKeepsLateAndLostUploadTargets(t *testing.T) {
	for _, scenario := range []string{"late", "lost", "wrong"} {
		t.Run(scenario, func(t *testing.T) {
			db, user := workerStore(t)
			ctx := t.Context()
			if err := db.DB.Create(&store.Meal{ID: 1, CreatorID: user, CreationDate: "2026-11-09", RecipeTitle: "Fixture", ImageStatus: "pending"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.DB.Create(&store.Session{Hash: "image-worker-session", UserID: &user, ExpiresUnix: time.Now().Add(time.Hour).Unix()}).Error; err != nil {
				t.Fatal(err)
			}
			journal, err := db.InitializeLifecycleJournal(ctx, db.Path+".lifecycle")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = journal.Close() }()
			var incarnation string
			if err := db.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", user).Scan(&incarnation); err != nil {
				t.Fatal(err)
			}
			provider := &lifecycleImageProvider{started: make(chan string, 1), release: make(chan struct{}), lost: scenario == "lost", wrong: scenario == "wrong"}
			var unblock sync.Once
			defer unblock.Do(func() { close(provider.release) })
			service := &Service{store: db, images: provider, ctx: ctx, options: Options{Lifecycle: journal, AssetProvider: "cloudinary/test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				service.processImage(imageTask{Owner: user, ID: 1, Content: domain.RecipeContent{Title: "Fixture"}})
			}()
			var target string
			select {
			case target = <-provider.started:
			case <-time.After(3 * time.Second):
				t.Fatal("worker never dispatched reserved upload")
			}
			if !strings.HasPrefix(target, "mychef/") {
				t.Fatal("worker dispatched untracked public ID")
			}
			if scenario != "late" {
				unblock.Do(func() { close(provider.release) })
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Fatal("worker did not finish")
				}
			}
			if _, err := journal.DeleteAccount(ctx, store.AccountActor{UserID: user, SessionHash: "image-worker-session", PasswordHash: "hash"}, strings.Repeat("c", 32), "DELETE", time.Now(), store.AccountDeletionOptions{}); err != nil {
				t.Fatal(err)
			}
			unblock.Do(func() { close(provider.release) })
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("late worker did not finish")
			}
			work, err := journal.WorkFor(ctx, incarnation)
			if err != nil || len(work) != 1 || work[0].Target != target || !work[0].Finished || work[0].Confirmed != (scenario == "late") {
				t.Fatal("worker lost or reassigned cleanup target", err)
			}
			var n int
			if err := db.SQL.QueryRow("SELECT COUNT(*) FROM meals WHERE creator_id=?", user).Scan(&n); err != nil || n != 0 {
				t.Fatal("late image recreated deleted meal", err)
			}
		})
	}
}

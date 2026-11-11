package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func lifecycleExportFixture(t *testing.T) (*Store, AccountActor) {
	t.Helper()
	s := lifecycleLegacyStore(t)
	ctx := t.Context()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	actor := AccountActor{UserID: 1, SessionHash: "export-session-secret", PasswordHash: "legacy-hash"}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO sessions(hash,user_id,expires_unix) VALUES(?,1,?)", actor.SessionHash, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(ctx, `UPDATE users SET preferences_json='{"diet":["vegetarian"],"allergens":[],"dislikes":[],"technique":[],"effort":"Quick"}' WHERE user_id=1;
 INSERT INTO menus VALUES(17,1,123),(18,2,124);
 INSERT INTO generations(id,user_id,request_key,request_hash,request_json,status,stage,menu_id) VALUES(11,1,'planning-key-secret','request-hash-secret','{"dates":["2026-11-09"],"adults":2,"children":0,"maxMinutes":30,"budget":"medium","region":"BE","creativity":3,"preferences":{"diet":["vegetarian"]}}','succeeded','finished',17);
 INSERT INTO meals(meal_id,creator_id,menu_id,creationdate,recipetitle,legacy_raw,cloudinary_public_id) VALUES(27,1,17,'2026-11-09','Owned dinner','{"password":"legacy-opaque-secret"}','provider-secret-id'),(28,2,18,'2026-11-09','OTHER-ACCOUNT-CANARY',NULL,NULL);
 INSERT INTO ingredients(pk_ingredients,recipe_id,ingredient_name,quantity,unit) VALUES(37,27,'Carrot','2','piece'),(38,28,'OTHER-INGREDIENT-CANARY','1','kg');
 INSERT INTO shopping_lists VALUES(1,'2026-11-09',2,'{"week":"2026-11-09","meals":[{"recipeId":"27","title":"Owned dinner"}],"items":[{"id":"raw-item-id","name":"Carrot","contributions":[{"recipeId":"27","title":"Owned dinner"}]}]}');
 INSERT INTO favorites(user_id,recipe_id,body,created_unix) VALUES(1,27,'{"id":"27","menuId":"17","title":"Owned dinner","ingredients":[{"id":"37","name":"Carrot"}],"legacyRaw":{"password":"favorite-opaque-secret"}}',125);
 INSERT INTO favorite_queue(user_id,recipe_id,week,body) VALUES(1,27,'2026-11-16','{"id":"27","menuId":"17","title":"Owned dinner"}');
 INSERT INTO favorite_swaps VALUES(1,'swap-key-secret','swap-hash-secret','{"id":"27","menuId":"17","title":"Owned dinner"}');
 INSERT INTO email_outbox(id,key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(51,'feedback/fixture','operator-secret@example.test','feedback-operator/v1','{"message":"outbox-payload-secret"}','queued',1,1,1);
 INSERT INTO feedback_reports(user_id,request_key,request_hash,reference,topic,message,priority,created_unix,outbox_id) VALUES(1,'feedback-key-secret',printf('%064d',0),printf('%032d',1),'other','Owned feedback message','standard',126,51);
 INSERT INTO feedback_ratings VALUES(1,'legacy_meal',27,4);
 INSERT INTO feedback_rating_receipts(user_id,request_key,request_hash,reference,created_unix) VALUES(1,'rating-key-secret',printf('%064d',0),printf('%032d',2),127);`); err != nil {
		t.Fatal(err)
	}
	return s, actor
}

func readLifecycleExport(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]json.RawMessage{}
	for _, file := range r.File {
		f, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name != "README.txt" && !json.Valid(data) {
			t.Fatal("invalid export JSON", file.Name)
		}
		out[file.Name] = data
	}
	return out
}
func TestAccountExportOwnedInventoryAndLocalRelationships(t *testing.T) {
	s, actor := lifecycleExportFixture(t)
	var b bytes.Buffer
	if err := s.WriteAccountExport(t.Context(), actor, &b, time.Now()); err != nil {
		t.Fatal(err)
	}
	files := readLifecycleExport(t, b.Bytes())
	for _, name := range []string{"manifest.json", "README.txt", "account.json", "preferences.json", "menus.json", "recipes.json", "ingredients.json", "planning.json", "shopping.json", "favorites.json", "favorite-queue.json", "favorite-swaps.json", "feedback.json", "ratings.json", "rating-receipts.json"} {
		if len(files[name]) == 0 {
			t.Fatal("missing export category", name)
		}
	}
	joined := ""
	for _, v := range files {
		joined += string(v)
	}
	for _, secret := range []string{"legacy-hash", "export-session-secret", "request-hash-secret", "planning-key-secret", "legacy-opaque-secret", "favorite-opaque-secret", "provider-secret-id", "operator-secret@example.test", "outbox-payload-secret", "feedback-key-secret", "rating-key-secret", "swap-key-secret", "swap-hash-secret", "OTHER-ACCOUNT-CANARY", "OTHER-INGREDIENT-CANARY"} {
		if strings.Contains(joined, secret) {
			t.Fatal("forbidden export canary", secret)
		}
	}
	for _, owned := range []string{"owner@example.test", "Owned dinner", "Carrot", "Owned feedback message", "vegetarian"} {
		if !strings.Contains(joined, owned) {
			t.Fatal("missing owned data", owned)
		}
	}
	var recipes, ingredients, ratings []map[string]any
	if err := json.Unmarshal(files["recipes.json"], &recipes); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["ingredients.json"], &ingredients); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["ratings.json"], &ratings); err != nil {
		t.Fatal(err)
	}
	if len(recipes) != 1 || len(ingredients) != 1 || len(ratings) != 1 || recipes[0]["id"] == "27" || ingredients[0]["recipeId"] != recipes[0]["id"] || ratings[0]["recipeId"] != recipes[0]["id"] {
		t.Fatal("export relationships are not local and consistent")
	}
}

func TestAccountExportRejectsStaleAuthorityBeforeWriting(t *testing.T) {
	for _, kind := range []string{"session", "password", "user"} {
		t.Run(kind, func(t *testing.T) {
			s, actor := lifecycleExportFixture(t)
			if kind == "session" {
				actor.SessionHash = "wrong"
			}
			if kind == "password" {
				actor.PasswordHash = "wrong"
			}
			if kind == "user" {
				actor.UserID = 2
			}
			var b bytes.Buffer
			if err := s.WriteAccountExport(t.Context(), actor, &b, time.Now()); !errors.Is(err, ErrAccountAccess) || b.Len() != 0 {
				t.Fatal("stale authority produced account bytes", err)
			}
		})
	}
}

type exportFailWriter struct{ bytes.Buffer }

func (w *exportFailWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		_, _ = w.Buffer.Write(p[:1])
	}
	return 0, io.ErrClosedPipe
}
func TestAccountExportFailureNeverFinalizesArchive(t *testing.T) {
	s, actor := lifecycleExportFixture(t)
	w := &exportFailWriter{}
	if err := s.WriteAccountExport(t.Context(), actor, w, time.Now()); err == nil {
		t.Fatal("failed download reported success")
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Bytes()), int64(w.Len())); err == nil {
		t.Fatal("failed download is a valid archive")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var b bytes.Buffer
	if err := s.WriteAccountExport(ctx, actor, &b, time.Now()); err == nil || b.Len() != 0 {
		t.Fatal("cancelled export produced data", err)
	}
	if _, err := s.SQL.ExecContext(t.Context(), "UPDATE favorites SET body='broken-json' WHERE user_id=1"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAccountExport(t.Context(), actor, &b, time.Now()); err == nil || b.Len() != 0 {
		t.Fatal("invalid stored data emitted partial archive before validation", err)
	}
}

type exportWriteHook struct {
	bytes.Buffer
	hook func()
	once bool
}

func (w *exportWriteHook) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		w.hook()
	}
	return w.Buffer.Write(p)
}
func TestAccountExportSnapshotDuringIndependentWrite(t *testing.T) {
	s, actor := lifecycleExportFixture(t)
	// Incompressible owned content forces actual writes before ZIP finalization.
	var input [65536]byte
	if _, err := rand.Read(input[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(t.Context(), "UPDATE meals SET instructions=? WHERE meal_id=27", hex.EncodeToString(input[:])); err != nil {
		t.Fatal(err)
	}
	other, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.SQL.ExecContext(t.Context(), "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	var concurrent error
	w := &exportWriteHook{hook: func() {
		_, concurrent = other.SQL.ExecContext(t.Context(), "UPDATE meals SET recipetitle='Changed during export' WHERE meal_id=27")
	}}
	if err := s.WriteAccountExport(t.Context(), actor, w, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !w.once {
		t.Fatal("stream did not reach writer")
	}
	files := readLifecycleExport(t, w.Bytes())
	if !strings.Contains(string(files["recipes.json"]), "Owned dinner") || strings.Contains(string(files["recipes.json"]), "Changed during export") {
		t.Fatal("export crossed snapshot versions")
	}
	// Either WAL concurrency or serialized access is permitted, but neither may
	// change the already authorized, consistent export snapshot.
	if concurrent != nil {
		if _, err := other.SQL.ExecContext(t.Context(), "UPDATE meals SET recipetitle='Changed during export' WHERE meal_id=27"); err != nil {
			t.Fatal(err)
		}
	}
	var title string
	if err := other.SQL.QueryRowContext(t.Context(), "SELECT recipetitle FROM meals WHERE meal_id=27").Scan(&title); err != nil || title != "Changed during export" {
		t.Fatal("independent update lost", err)
	}
}

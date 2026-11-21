package auth

import (
	"encoding/json"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestRenameKeepsServerOwnedPlanningSettings(t *testing.T) {
	manager := New(openPasswordTestStore(t))
	user := usernamePolicyRegister(t, manager, "planning-profile@example.test", "Original")
	err := manager.Store.DB.Model(&user).Update("preferences_json", `{"planningTimeZone":"Europe/Brussels"}`).Error
	if err != nil {
		t.Fatal(err)
	}
	profile := usernamePolicyProfile(user, "Updated")
	profile.Preferences.Effort = "Quick"
	if _, err := manager.Update(t.Context(), user.ID, profile); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Login(t.Context(), "updated", "synthetic pantry account password")
	if err != nil || got.ID != user.ID || got.PreferencesJSON == nil {
		t.Fatalf("renamed account did not resolve: %v", err)
	}
	var preferences map[string]any
	if err := json.Unmarshal([]byte(*got.PreferencesJSON), &preferences); err != nil {
		t.Fatal(err)
	}
	if preferences["planningTimeZone"] != "Europe/Brussels" || preferences["effort"] != "Quick" {
		t.Fatal("profile update did not retain the server-owned setting and apply the user preference")
	}
	var old store.User
	if err := manager.Store.DB.Where("login_username_key=?", store.UsernameKey("Original")).First(&old).Error; old.ID != 0 || err == nil {
		t.Fatal("renamed account retained its old username key")
	}
}

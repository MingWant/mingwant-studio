package service

import (
	"fmt"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreateAuthSessionCleansExpiredAndCapsActiveSessions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserIdentity{}, &model.AuthSession{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "user-1", Username: "canvas-user", DisplayName: "Canvas User", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index := 0; index < maxActiveAuthSessions; index++ {
		session := model.AuthSession{
			ID: fmt.Sprintf("old-%02d", index), UserID: user.ID, TokenHash: hashToken(fmt.Sprintf("token-%02d", index)),
			ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(time.Duration(index-maxActiveAuthSessions) * time.Minute), UpdatedAt: now,
		}
		if err := db.Create(&session).Error; err != nil {
			t.Fatal(err)
		}
	}
	expired := model.AuthSession{ID: "expired", UserID: user.ID, TokenHash: hashToken("expired"), ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}

	result, err := (&Service{repo: repository.New(db)}).createAuthSession(&user)
	if err != nil {
		t.Fatal(err)
	}
	currentID, _ := parseSessionCookie(result.Session)
	var count int64
	if err := db.Model(&model.AuthSession{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != maxActiveAuthSessions {
		t.Fatalf("session count = %d, want %d", count, maxActiveAuthSessions)
	}
	for _, id := range []string{currentID, "old-19"} {
		if err := db.First(&model.AuthSession{}, "id = ?", id).Error; err != nil {
			t.Fatalf("expected session %q to remain: %v", id, err)
		}
	}
	for _, id := range []string{"expired", "old-00"} {
		if err := db.First(&model.AuthSession{}, "id = ?", id).Error; err == nil {
			t.Fatalf("expected session %q to be removed", id)
		}
	}
}

func TestRevokeOtherAuthSessionsKeepsCurrentSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "user-1", Username: "canvas-user", DisplayName: "Canvas User", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sessions := []model.AuthSession{
		{ID: "current", UserID: user.ID, TokenHash: hashToken("secret"), ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: "other-1", UserID: user.ID, TokenHash: hashToken("other-1"), ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: "other-2", UserID: user.ID, TokenHash: hashToken("other-2"), ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db)}
	revoked, err := svc.RevokeOtherAuthSessions("current.secret")
	if err != nil {
		t.Fatal(err)
	}
	if revoked != 2 {
		t.Fatalf("revoked = %d, want 2", revoked)
	}
	if _, err := svc.CurrentUser("current.secret"); err != nil {
		t.Fatalf("current session should remain valid: %v", err)
	}
}

func TestPublicAuthUserIncludesLinuxDOIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserIdentity{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "user-1", Username: "canvas-user", DisplayName: "Canvas User", Role: model.UserRoleUser, Status: model.UserStatusActive}
	identity := model.UserIdentity{ID: "identity-1", UserID: user.ID, Provider: "linuxdo", Subject: "123456", ProviderUsername: "linux-user", AvatarURL: "https://example.com/avatar.png"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}

	result, err := (&Service{repo: repository.New(db)}).PublicAuthUser(&user)
	if err != nil {
		t.Fatal(err)
	}
	if result.AvatarURL != identity.AvatarURL || result.IdentityProvider != "linuxdo" || result.IdentityID != identity.Subject || result.IdentityUsername != identity.ProviderUsername {
		t.Fatalf("PublicAuthUser() = %#v", result)
	}
}

func TestPublicAuthUserKeepsLocalUserWithoutIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.UserIdentity{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "user-1", Username: "local-user", DisplayName: "Local User"}

	result, err := (&Service{repo: repository.New(db)}).PublicAuthUser(&user)
	if err != nil {
		t.Fatal(err)
	}
	if result.Username != user.Username || result.AvatarURL != "" || result.IdentityProvider != "" || result.IdentityID != "" {
		t.Fatalf("PublicAuthUser() = %#v", result)
	}
}

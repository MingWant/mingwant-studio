package service

import (
	"context"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newChannelConsistencyTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ModelChannel{}, &model.ChannelModel{}, &model.AdminAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db)}, db
}

func TestPublicChannelDoesNotRestoreDisabledLegacyModel(t *testing.T) {
	channel := model.ModelChannel{ID: "channel-1", Scope: model.ChannelScopeSystem, ModelsJSON: `["disabled-model"]`}
	item := model.ChannelModel{ID: "model-1", ChannelID: channel.ID, ModelKey: "disabled-model", Enabled: false}

	public := publicChannel(channel, false, []model.ChannelModel{item})
	if len(public.Models) != 0 {
		t.Fatalf("public models = %#v", public.Models)
	}
}

func TestEnsureSystemChannelModelsRepairsLegacyListFromAuthoritativeRows(t *testing.T) {
	svc, db := newChannelConsistencyTestService(t)
	channel := model.ModelChannel{
		ID: "channel-repair", UserID: "admin-1", Scope: model.ChannelScopeSystem, Enabled: true,
		Name: "text", BaseURL: "https://example.com/v1", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion,
		ModelsJSON: `["enabled-model","stale-model"]`,
	}
	items := []model.ChannelModel{
		{ID: "model-enabled", ChannelID: channel.ID, ModelKey: "enabled-model", Enabled: true},
		{ID: "model-disabled", ChannelID: channel.ID, ModelKey: "stale-model", Enabled: false},
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureSystemChannelModels(); err != nil {
		t.Fatal(err)
	}
	var stored model.ModelChannel
	if err := db.First(&stored, "id = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ModelsJSON != `["enabled-model"]` {
		t.Fatalf("models_json = %q", stored.ModelsJSON)
	}
}

func TestEnsureSystemChannelModelsDoesNotResurrectDeletedHistory(t *testing.T) {
	svc, db := newChannelConsistencyTestService(t)
	channel := model.ModelChannel{
		ID: "channel-deleted", UserID: "admin-1", Scope: model.ChannelScopeSystem, Enabled: true,
		Name: "text", BaseURL: "https://example.com/v1", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion,
		ModelsJSON: `["retired-model"]`,
	}
	item := model.ChannelModel{ID: "model-retired", ChannelID: channel.ID, ModelKey: "retired-model", Enabled: false}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureSystemChannelModels(); err != nil {
		t.Fatal(err)
	}
	var activeCount int64
	if err := db.Model(&model.ChannelModel{}).Where("channel_id = ?", channel.ID).Count(&activeCount).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.ModelChannel
	if err := db.First(&stored, "id = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if activeCount != 0 || stored.ModelsJSON != `[]` {
		t.Fatalf("active models = %d, models_json = %q", activeCount, stored.ModelsJSON)
	}
}

func TestSaveAdminChannelModelRollsBackWhenLegacyListCannotPersist(t *testing.T) {
	svc, db := newChannelConsistencyTestService(t)
	channel := model.ModelChannel{
		ID: "channel-rollback", UserID: "admin-1", Scope: model.ChannelScopeSystem, Enabled: true,
		Name: "text", BaseURL: "https://example.com/v1", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion,
		ModelsJSON: `["text-model"]`,
	}
	item := model.ChannelModel{
		ID: "model-rollback", ChannelID: channel.ID, ModelKey: "text-model", DisplayName: "Text Model",
		Capability: "text", Protocol: model.ChannelInterfaceChatCompletion, BillingMode: "fixed_request",
		UnitPriceMicrocredits: 100, PriceConfigured: true, Enabled: true, PriceVersion: 1,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_empty_channel_models BEFORE UPDATE OF models_json ON model_channels WHEN NEW.models_json = '[]' BEGIN SELECT RAISE(ABORT, 'blocked'); END`).Error; err != nil {
		t.Fatal(err)
	}

	disabled := false
	_, err := svc.SaveAdminChannelModel(
		&model.User{ID: "admin-1", Role: model.UserRoleAdmin}, channel.ID, item.ID,
		ChannelModelRequest{
			ModelKey: item.ModelKey, DisplayName: item.DisplayName, Capability: item.Capability,
			Protocol: string(item.Protocol), BillingMode: item.BillingMode, UnitPriceMicrocredits: item.UnitPriceMicrocredits,
			PriceConfigured: item.PriceConfigured, Enabled: &disabled,
		},
	)
	if err == nil {
		t.Fatal("SaveAdminChannelModel() error = nil")
	}
	var stored model.ChannelModel
	if err := db.First(&stored, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || stored.PriceVersion != item.PriceVersion {
		t.Fatalf("model changed after rollback: %#v", stored)
	}
}

func TestNormalizedChannelModelNamesDropsEmptyPrefixesAndDuplicates(t *testing.T) {
	models := normalizedChannelModelNames([]string{" models/text-a ", "text-a", "models/", "", "text-b"})
	if len(models) != 2 || models[0] != "text-a" || models[1] != "text-b" {
		t.Fatalf("normalized models = %#v", models)
	}
}

func TestResolveProviderConfigRejectsLegacyAuthorizedButDisabledModel(t *testing.T) {
	svc, db := newChannelConsistencyTestService(t)
	channel := model.ModelChannel{
		ID: "channel-disabled", UserID: "admin-1", Scope: model.ChannelScopeSystem, Enabled: true,
		Name: "text", BaseURL: "https://example.com/v1", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion,
		ModelsJSON: `["disabled-model"]`,
	}
	item := model.ChannelModel{
		ID: "model-disabled-provider", ChannelID: channel.ID, ModelKey: "disabled-model", Capability: "text",
		Protocol: model.ChannelInterfaceChatCompletion, BillingMode: "fixed_request", Enabled: false,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.resolveProviderConfig(context.Background(), providerConfig{ChannelID: channel.ID, Model: item.ModelKey}); err == nil {
		t.Fatal("resolveProviderConfig() accepted disabled model from legacy models_json")
	}
}

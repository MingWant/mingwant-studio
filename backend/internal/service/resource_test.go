package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGetOSSObjectRangeUsesCallerContext(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := getOSSObjectRange(ctx, ossSettingValue{
		Endpoint: server.URL, Bucket: "127", AccessKeyID: "access-id", AccessKeySecret: "secret",
	}, "users/u-1/reference.png", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("getOSSObjectRange() error = %v, want context canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("OSS server calls = %d, want 0", calls.Load())
	}
}

func TestSignedOSSObjectURLUsesExpiringQuerySignature(t *testing.T) {
	expiresAt := time.Unix(1800000000, 0)
	value, err := signedOSSObjectURL(ossSettingValue{
		Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		AccessKeyID: "access-id", AccessKeySecret: "secret-value",
	}, "users/u-1/image/test image.png", expiresAt)
	if err != nil {
		t.Fatalf("signedOSSObjectURL() error = %v", err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Host != "private-bucket.oss-cn-test.aliyuncs.com" || query.Get("OSSAccessKeyId") != "access-id" || query.Get("Expires") != "1800000000" || query.Get("Signature") == "" {
		t.Fatalf("signed URL = %q", value)
	}
	if strings.Contains(value, "secret-value") {
		t.Fatalf("signed URL leaked access key secret: %q", value)
	}
}

func TestDirectResourceURLChecksOwnershipAndSignsOSSResource(t *testing.T) {
	svc := newResourceTestService(t)
	settingJSON, _ := json.Marshal(ossSettingValue{
		Enabled: true, Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		AccessKeyID: "access-id", AccessKeySecret: "secret-value",
	})
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: ossSettingKey, ValueJSON: string(settingJSON)}); err != nil {
		t.Fatal(err)
	}
	resource := model.Resource{
		ID: "resource-direct", UserID: "user-1", Kind: "image", Status: model.ResourceStatusReady,
		Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		ObjectKey: "users/user-1/image/direct.png", MimeType: "image/png",
	}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	value, err := svc.DirectResourceURL("user-1", resource.ID)
	if err != nil || !strings.Contains(value, "Signature=") {
		t.Fatalf("DirectResourceURL() = %q, %v", value, err)
	}
	if _, err := svc.DirectResourceURL("other-user", resource.ID); err == nil {
		t.Fatal("DirectResourceURL() allowed another user's resource")
	}
}

func TestNormalizeSingleByteRange(t *testing.T) {
	tests := map[string]string{
		"bytes=0-1023":       "bytes=0-1023",
		"bytes=1024-":        "bytes=1024-",
		"bytes=-2048":        "bytes=-2048",
		"bytes=0-1,10-20":    "",
		"items=0-10":         "",
		"bytes=invalid-1024": "",
	}
	for input, expected := range tests {
		if actual := normalizeSingleByteRange(input); actual != expected {
			t.Fatalf("normalizeSingleByteRange(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestHydrateNewAPIChannel1ResourceUsesSignedOSSURL(t *testing.T) {
	svc := newResourceTestService(t)
	settingJSON, _ := json.Marshal(ossSettingValue{
		Enabled: true, Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		AccessKeyID: "access-id", AccessKeySecret: "secret-value",
	})
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: ossSettingKey, ValueJSON: string(settingJSON)}); err != nil {
		t.Fatal(err)
	}
	resource := model.Resource{
		ID: "resource-1", UserID: "user-1", Kind: "image", Status: model.ResourceStatusReady,
		Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		ObjectKey: "users/user-1/image/reference.png", MimeType: "image/png",
	}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	media := providerMedia{StorageKey: "resource:resource-1", DataURL: "data:image/png;base64,old"}
	if err := svc.hydrateProviderMedia(context.Background(), "user-1", &media, true); err != nil {
		t.Fatalf("hydrateProviderMedia() error = %v", err)
	}
	if !strings.HasPrefix(media.URL, "https://private-bucket.oss-cn-test.aliyuncs.com/") || media.DataURL != "" || !strings.Contains(media.URL, "Signature=") {
		t.Fatalf("media = %#v", media)
	}
	if err := svc.hydrateProviderMedia(context.Background(), "other-user", &providerMedia{StorageKey: "resource:resource-1"}, true); err == nil {
		t.Fatal("hydrateProviderMedia() allowed another user's resource")
	}
}

func TestHydrateNewAPIChannel1ResourceRejectsLocalStorage(t *testing.T) {
	svc := newResourceTestService(t)
	resource := model.Resource{ID: "resource-local", UserID: "user-1", Status: model.ResourceStatusReady, Provider: "local", ObjectKey: "local.png"}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	err := svc.hydrateProviderMedia(context.Background(), "user-1", &providerMedia{StorageKey: "resource:resource-local"}, true)
	if err == nil || !strings.Contains(err.Error(), "启用 OSS") {
		t.Fatalf("hydrateProviderMedia() error = %v", err)
	}
}

func TestActiveResourceOSSSettingPrefersUserVersion(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	svc := newResourceTestService(t)
	systemJSON, _ := json.Marshal(ossSettingValue{Enabled: true, Provider: "aliyun", Endpoint: server.URL, Bucket: "system", AccessKeyID: "system-id", AccessKeySecret: "system-secret"})
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: ossSettingKey, ValueJSON: string(systemJSON)}); err != nil {
		t.Fatal(err)
	}
	actor := &model.User{ID: "user-1"}
	created, err := svc.UpdateUserOSSSetting(actor, OSSSettingRequest{Enabled: true, Provider: "aliyun", Endpoint: server.URL, Bucket: "user", AccessKeyID: "user-id", AccessKeySecret: "user-secret"})
	if err != nil {
		t.Fatal(err)
	}
	setting, settingID, useOSS, err := svc.activeResourceOSSSetting(actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !useOSS || settingID == "" || setting.Bucket != "user" || !created.Enabled {
		t.Fatalf("activeResourceOSSSetting() = %#v, %q, %v", setting, settingID, useOSS)
	}
}

func TestUserOSSSettingVersionsKeepHistoricalSecrets(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	svc := newResourceTestService(t)
	actor := &model.User{ID: "user-1"}
	if _, err := svc.UpdateUserOSSSetting(actor, OSSSettingRequest{Enabled: true, Provider: "aliyun", Endpoint: server.URL, Bucket: "old", AccessKeyID: "old-id", AccessKeySecret: "old-secret"}); err != nil {
		t.Fatal(err)
	}
	oldSetting, _, err := svc.readUserOSSSetting(actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUserOSSSetting(actor, OSSSettingRequest{Enabled: true, Provider: "aliyun", Endpoint: server.URL, Bucket: "new", AccessKeyID: "new-id", AccessKeySecret: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	_, oldValue, err := svc.readUserOSSSettingByID(actor.ID, oldSetting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldValue.Bucket != "old" || oldValue.AccessKeySecret != "old-secret" {
		t.Fatalf("historical setting = %#v", oldValue)
	}
}

func newResourceTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.UserOSSSetting{}, &model.UserDailyUploadUsage{}, &model.UserDailyActivity{}, &model.Resource{}, &model.SessionFile{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db), dataDir: t.TempDir()}
}

func TestLegacyMediaMigrationSkipsInvalidDataURL(t *testing.T) {
	svc := &Service{}
	input := map[string]interface{}{
		"history": []interface{}{
			map[string]interface{}{"content": "data:video/mp4;base64,broken"},
		},
	}

	result, err := svc.persistLegacyGeneratedMediaResult("user-1", input)
	if err != nil {
		t.Fatalf("persistLegacyGeneratedMediaResult() error = %v", err)
	}
	history := result["history"].([]interface{})
	content := history[0].(map[string]interface{})["content"]
	if content != "data:video/mp4;base64,broken" {
		t.Fatalf("invalid legacy content changed to %v", content)
	}
}

func TestGeneratedMediaRejectsInvalidDataURL(t *testing.T) {
	svc := &Service{}
	_, err := svc.persistGeneratedMediaResult("user-1", map[string]interface{}{
		"content": "data:video/mp4;base64,broken",
	})
	if err == nil {
		t.Fatal("persistGeneratedMediaResult() error = nil, want invalid data URL error")
	}
}

func TestPersistGeneratedMediaAppliesStoredFileQuota(t *testing.T) {
	svc := newResourceTestService(t)
	if err := svc.repo.Create(&model.Resource{
		ID:     "existing",
		UserID: "user-1",
		Status: model.ResourceStatusReady,
		Size:   gigabytes(defaultRuntimePolicy().Resource.StoredFileGB) - 1,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.persistGeneratedMediaResult("user-1", map[string]interface{}{
		"image": map[string]interface{}{"dataUrl": "data:image/png;base64,YQ=="},
	})
	if err == nil || !strings.Contains(err.Error(), "2GB 上限") {
		t.Fatalf("persistGeneratedMediaResult() error = %v", err)
	}
}

func TestPersistTaskGeneratedMediaPreflightsAllDataURLs(t *testing.T) {
	svc := newResourceTestService(t)
	_, err := svc.persistTaskGeneratedMediaResult(model.Task{ID: "task-1", UserID: "user-1", BillingOrderID: "order-1", Attempts: 1}, map[string]interface{}{
		"images": []interface{}{
			map[string]interface{}{"dataUrl": "data:image/png;base64,YQ=="},
			map[string]interface{}{"dataUrl": "data:image/png;base64,broken"},
		},
	})
	if err == nil {
		t.Fatal("persistTaskGeneratedMediaResult() error = nil, want invalid data URL error")
	}
	resources, listErr := svc.repo.Resources("user-1", 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(resources) != 0 {
		t.Fatalf("resources = %d, want 0 after preflight failure", len(resources))
	}
}

func TestPersistTaskGeneratedMediaReusesOnlyCurrentAttempt(t *testing.T) {
	svc := newResourceTestService(t)
	payload := func() map[string]interface{} {
		return map[string]interface{}{"images": []interface{}{map[string]interface{}{"dataUrl": "data:image/png;base64,YQ=="}}}
	}
	task := model.Task{ID: "task-1", UserID: "user-1", BillingOrderID: "order-1", Attempts: 1}
	first, err := svc.persistTaskGeneratedMediaResult(task, payload())
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.persistTaskGeneratedMediaResult(task, payload())
	if err != nil {
		t.Fatal(err)
	}
	firstID := generatedResultResourceID(t, first)
	if secondID := generatedResultResourceID(t, second); secondID != firstID {
		t.Fatalf("same attempt resource = %q, want %q", secondID, firstID)
	}

	task.BillingOrderID = "order-2"
	task.Attempts = 2
	third, err := svc.persistTaskGeneratedMediaResult(task, payload())
	if err != nil {
		t.Fatal(err)
	}
	if thirdID := generatedResultResourceID(t, third); thirdID == firstID {
		t.Fatalf("new paid attempt reused old resource %q", thirdID)
	}
	resources, err := svc.repo.Resources("user-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(resources))
	}
}

func TestUserStoredFileBytesCountsOnlyReadyResources(t *testing.T) {
	svc := newResourceTestService(t)
	for _, resource := range []model.Resource{
		{ID: "ready", UserID: "user-1", Status: model.ResourceStatusReady, Size: 7},
		{ID: "pending", UserID: "user-1", Status: model.ResourceStatusPending, Size: 11},
		{ID: "failed", UserID: "user-1", Status: model.ResourceStatusFailed, Size: 13},
	} {
		if err := svc.repo.CreateResource(&resource); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.repo.UserStoredFileBytes("user-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Fatalf("UserStoredFileBytes() = %d, want 7", got)
	}
}

func TestPersistTaskGeneratedMediaRecoversStalePendingResource(t *testing.T) {
	svc := newResourceTestService(t)
	day := time.Now().UTC().Format("2006-01-02")
	if err := svc.repo.Create(&model.UserDailyUploadUsage{ID: "user-1:" + day, UserID: "user-1", Day: day, Bytes: 1}); err != nil {
		t.Fatal(err)
	}
	resource := model.Resource{
		ID: "pending-resource", UserID: "user-1", Kind: "image", Status: model.ResourceStatusPending, Provider: "local",
		ObjectKey: "users/user-1/image/pending.png", MimeType: "image/png", Size: 1,
		SourceTaskID: "task-1", SourceAttempt: "billing:order-1", SourcePath: "$/images/0",
		ContentSHA256: "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb", QuotaDay: day,
		CreatedAt: time.Now().Add(-4 * time.Minute), UpdatedAt: time.Now().Add(-4 * time.Minute),
	}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	result, err := svc.persistTaskGeneratedMediaResult(model.Task{ID: "task-1", UserID: "user-1", BillingOrderID: "order-1", Attempts: 1}, map[string]interface{}{
		"images": []interface{}{map[string]interface{}{"dataUrl": "data:image/png;base64,YQ=="}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id := generatedResultResourceID(t, result); id != resource.ID {
		t.Fatalf("recovered resource = %q, want %q", id, resource.ID)
	}
	stored, err := svc.repo.ResourceForUser("user-1", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.ResourceStatusReady {
		t.Fatalf("resource status = %s", stored.Status)
	}
	usage, err := svc.repo.DailyUploadBytes("user-1", day)
	if err != nil {
		t.Fatal(err)
	}
	if usage != 1 {
		t.Fatalf("daily upload bytes = %d, want 1", usage)
	}
}

func TestPersistTaskGeneratedMediaReturnsPartialCheckpointAndRecoversPendingObject(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	var putCount atomic.Int32
	var failSecond atomic.Bool
	failSecond.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		current := putCount.Add(1)
		if failSecond.Load() && current == 2 {
			http.Error(response, "temporary storage failure", http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("ETag", `"stored"`)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := newResourceTestService(t)
	settingJSON, _ := json.Marshal(ossSettingValue{
		Enabled: true, Provider: "aliyun", Endpoint: server.URL, Bucket: "127",
		AccessKeyID: "access-id", AccessKeySecret: "secret-value",
	})
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: ossSettingKey, ValueJSON: string(settingJSON)}); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: "task-partial", UserID: "user-1", BillingOrderID: "order-partial", Attempts: 1}
	payload := map[string]interface{}{
		"images": []interface{}{
			map[string]interface{}{"dataUrl": "data:image/png;base64,YQ=="},
			map[string]interface{}{"dataUrl": "data:image/png;base64,Yg=="},
		},
	}
	partial, err := svc.persistTaskGeneratedMediaResult(task, payload)
	if err == nil {
		t.Fatal("persistTaskGeneratedMediaResult() error = nil, want second object write failure")
	}
	images := partial["images"].([]interface{})
	firstID, _ := images[0].(map[string]interface{})["resourceId"].(string)
	if firstID == "" {
		t.Fatalf("first persisted image = %#v", images[0])
	}
	if secondID, _ := images[1].(map[string]interface{})["resourceId"].(string); secondID != "" {
		t.Fatalf("failed second image unexpectedly exposed resource %q", secondID)
	}
	resources, listErr := svc.repo.Resources(task.UserID, 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(resources) != 2 {
		t.Fatalf("resources after partial write = %d, want ready + pending", len(resources))
	}
	var pendingID string
	for _, resource := range resources {
		if resource.Status == model.ResourceStatusPending {
			pendingID = resource.ID
		}
	}
	if pendingID == "" {
		t.Fatalf("resources after partial write = %#v", resources)
	}

	failSecond.Store(false)
	recovered, err := svc.persistRecoveringTaskGeneratedMediaResult(task, partial)
	if err != nil {
		t.Fatal(err)
	}
	recoveredImages := recovered["images"].([]interface{})
	if recoveredFirstID, _ := recoveredImages[0].(map[string]interface{})["resourceId"].(string); recoveredFirstID != firstID {
		t.Fatalf("first resource changed from %q to %q", firstID, recoveredFirstID)
	}
	if recoveredSecondID, _ := recoveredImages[1].(map[string]interface{})["resourceId"].(string); recoveredSecondID != pendingID {
		t.Fatalf("recovered pending resource = %q, want %q", recoveredSecondID, pendingID)
	}
	resources, listErr = svc.repo.Resources(task.UserID, 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(resources) != 2 || resources[0].Status != model.ResourceStatusReady || resources[1].Status != model.ResourceStatusReady {
		t.Fatalf("resources after recovery = %#v", resources)
	}
	day := time.Now().UTC().Format("2006-01-02")
	dailyBytes, usageErr := svc.repo.DailyUploadBytes(task.UserID, day)
	if usageErr != nil {
		t.Fatal(usageErr)
	}
	if dailyBytes != 2 {
		t.Fatalf("daily upload bytes = %d, want 2 without recovery double count", dailyBytes)
	}
}

func generatedResultResourceID(t *testing.T, result map[string]interface{}) string {
	t.Helper()
	images, ok := result["images"].([]interface{})
	if !ok || len(images) != 1 {
		t.Fatalf("images = %#v", result["images"])
	}
	image, ok := images[0].(map[string]interface{})
	if !ok {
		t.Fatalf("image = %#v", images[0])
	}
	id, _ := image["resourceId"].(string)
	if id == "" {
		t.Fatalf("resourceId = %#v", image["resourceId"])
	}
	return id
}

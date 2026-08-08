package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const providerResourceURLTTL = 4 * time.Hour
const directResourceURLTTL = 5 * time.Minute

var errInvalidGeneratedDataURL = errors.New("生成内容 data URL 无效")

type ResourceStream struct {
	Resource      *model.Resource
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ContentRange  string
	AcceptRanges  string
}

func (s *Service) Resources(userID string, limit int) ([]model.Resource, error) {
	resources, err := s.repo.Resources(userID, limit)
	for index := range resources {
		resources[index].PublicURL = ""
	}
	return resources, err
}

func (s *Service) Resource(userID string, id string) (*model.Resource, error) {
	resource, err := s.repo.ResourceForUser(userID, id)
	if resource != nil {
		resource.PublicURL = ""
	}
	return resource, err
}

// DirectResourceURL 先校验资源归属，再为私有 OSS 对象签发短时下载地址；本地资源继续由应用流式读取。
func (s *Service) DirectResourceURL(userID string, id string) (string, error) {
	resource, err := s.repo.ResourceForUser(userID, id)
	if err != nil {
		return "", err
	}
	if resource.Status != model.ResourceStatusReady {
		return "", BadAuthRequest("资源尚未上传完成")
	}
	if resource.Provider == "local" {
		return "", BadAuthRequest("当前资源未存储在 OSS")
	}
	setting, err := s.ossSettingForResource(userID, resource)
	if err != nil {
		return "", err
	}
	setting.Provider = firstNonEmpty(resource.Provider, setting.Provider)
	setting.Endpoint = firstNonEmpty(resource.Endpoint, setting.Endpoint)
	setting.Bucket = firstNonEmpty(resource.Bucket, setting.Bucket)
	return signedOSSObjectURL(setting, resource.ObjectKey, time.Now().Add(directResourceURLTTL))
}

func (s *Service) UploadResource(userID string, header *multipart.FileHeader, kind string, width int, height int, durationMs int64) (*model.Resource, error) {
	if header == nil {
		return nil, BadAuthRequest("请选择要上传的文件")
	}
	day, err := s.reserveUserUploadQuota(userID, header.Size)
	if err != nil {
		return nil, err
	}
	file, err := header.Open()
	if err != nil {
		s.releaseUserUploadQuota(userID, day, header.Size)
		return nil, err
	}
	defer file.Close()

	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	mimeType = detectUploadedMimeType(file, header.Filename, mimeType)
	resource, err := s.storeResource(userID, kind, header.Filename, mimeType, header.Size, width, height, durationMs, file)
	if err != nil {
		s.releaseUserUploadQuota(userID, day, header.Size)
	} else {
		s.commitUserUploadQuota(userID, header.Size)
	}
	return resource, err
}

func detectUploadedMimeType(file multipart.File, fileName string, declared string) string {
	declared = strings.TrimSpace(strings.Split(declared, ";")[0])
	if declared != "" && declared != "application/octet-stream" {
		return declared
	}
	buffer := make([]byte, 512)
	read, _ := file.Read(buffer)
	_, _ = file.Seek(0, io.SeekStart)
	if detected := http.DetectContentType(buffer[:read]); detected != "" && detected != "application/octet-stream" {
		return strings.TrimSpace(strings.Split(detected, ";")[0])
	}
	if fromExtension := mime.TypeByExtension(filepath.Ext(fileName)); fromExtension != "" {
		return strings.TrimSpace(strings.Split(fromExtension, ";")[0])
	}
	return "application/octet-stream"
}

func (s *Service) ImportResourceURL(userID string, rawURL string, kind string, width int, height int, durationMs int64) (*model.Resource, error) {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	payload, err := downloadRemoteResource(rawURL, megabytes(policy.Resource.ResourceUploadMB))
	if err != nil {
		return nil, err
	}
	kind = normalizeResourceKind(kind, payload.mimeType)
	if kind == "image" && (width <= 0 || height <= 0) {
		if decodedWidth, decodedHeight := imageDimensions(payload.data); decodedWidth > 0 && decodedHeight > 0 {
			width = decodedWidth
			height = decodedHeight
		}
	}
	size := int64(len(payload.data))
	day, err := s.reserveUserUploadQuota(userID, size)
	if err != nil {
		return nil, err
	}
	resource, err := s.storeResource(userID, kind, payload.fileName, payload.mimeType, size, width, height, durationMs, bytes.NewReader(payload.data))
	if err != nil {
		s.releaseUserUploadQuota(userID, day, size)
	} else {
		s.commitUserUploadQuota(userID, size)
	}
	return resource, err
}

func (s *Service) OpenResource(userID string, id string) (*model.Resource, io.ReadCloser, error) {
	return s.OpenResourceContext(context.Background(), userID, id)
}

func (s *Service) OpenResourceContext(ctx context.Context, userID string, id string) (*model.Resource, io.ReadCloser, error) {
	stream, err := s.OpenResourceRangeContext(ctx, userID, id, "")
	if err != nil {
		return nil, nil, err
	}
	return stream.Resource, stream.Body, nil
}

func (s *Service) OpenResourceRange(userID string, id string, rangeHeader string) (*ResourceStream, error) {
	return s.OpenResourceRangeContext(context.Background(), userID, id, rangeHeader)
}

func (s *Service) OpenResourceRangeContext(ctx context.Context, userID string, id string, rangeHeader string) (*ResourceStream, error) {
	resource, err := s.repo.ResourceForUser(userID, id)
	if err != nil {
		return nil, err
	}
	if resource.Status != model.ResourceStatusReady {
		return nil, BadAuthRequest("资源尚未上传完成")
	}
	if resource.Provider == "local" {
		body, err := os.Open(filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey)))
		if err != nil {
			return nil, err
		}
		return &ResourceStream{Resource: resource, Body: body, StatusCode: http.StatusOK, ContentLength: resource.Size, AcceptRanges: "bytes"}, nil
	}
	setting, err := s.ossSettingForResource(userID, resource)
	if err != nil {
		return nil, err
	}
	if setting.AccessKeyID == "" || setting.AccessKeySecret == "" {
		return nil, errors.New("OSS 访问密钥不可用")
	}
	setting.Provider = firstNonEmpty(resource.Provider, setting.Provider)
	setting.Endpoint = firstNonEmpty(resource.Endpoint, setting.Endpoint)
	setting.Bucket = firstNonEmpty(resource.Bucket, setting.Bucket)
	stream, err := getOSSObjectRange(ctx, setting, resource.ObjectKey, normalizeSingleByteRange(rangeHeader))
	if err != nil {
		return nil, err
	}
	return &ResourceStream{Resource: resource, Body: stream.body, StatusCode: stream.statusCode, ContentLength: stream.contentLength, ContentRange: stream.contentRange, AcceptRanges: stream.acceptRanges}, nil
}

func (s *Service) storeResource(userID string, kind string, fileName string, mimeType string, size int64, width int, height int, durationMs int64, body io.Reader) (*model.Resource, error) {
	return s.storeResourceWithSource(userID, kind, fileName, mimeType, size, width, height, durationMs, body, generatedResourceSource{})
}

func (s *Service) storeResourceWithSource(userID string, kind string, fileName string, mimeType string, size int64, width int, height int, durationMs int64, body io.Reader, source generatedResourceSource) (*model.Resource, error) {
	now := time.Now()
	kind = normalizeResourceKind(kind, mimeType)
	setting, storageSettingID, useOSS, err := s.activeResourceOSSSetting(userID)
	if err != nil {
		return nil, err
	}
	provider := "local"
	objectKey := localObjectKey(userID, kind, fileName, now)
	resource := model.Resource{
		ID: newID(), UserID: userID, Kind: kind, Status: model.ResourceStatusPending, Provider: provider, ObjectKey: objectKey,
		MimeType: mimeType, Size: size, Width: width, Height: height, DurationMs: durationMs,
		SourceTaskID: source.TaskID, SourceAttempt: source.Attempt, SourcePath: source.Path, ContentSHA256: source.ContentSHA256, QuotaDay: source.QuotaDay,
		CreatedAt: now, UpdatedAt: now,
	}
	if useOSS {
		provider = setting.Provider
		objectKey = ossObjectKey(setting, userID, kind, fileName, now)
		resource.Provider = provider
		resource.Endpoint = setting.Endpoint
		resource.Bucket = setting.Bucket
		resource.StorageSettingID = storageSettingID
		resource.ObjectKey = objectKey
	}
	if err := s.repo.CreateResource(&resource); err != nil {
		return nil, err
	}
	etag, err := s.writeResourceBody(userID, &resource, body)
	resource.UpdatedAt = time.Now()
	if err != nil {
		resource.Status = model.ResourceStatusFailed
		resource.Error = "资源写入失败，请联系管理员按资源 ID 核对服务端日志"
		if source.TaskID != "" {
			// 供应商结果无法重新获取；任务生成资源保留 pending 身份，后续只覆写同一对象而不再次调用供应商。
			resource.Status = model.ResourceStatusPending
			resource.Error = "任务生成资源写入中断，可从任务详情恢复已保存结果"
		}
		log.Printf("resource upload failed resource=%s user=%s provider=%s: %v", resource.ID, userID, provider, err)
		saveErr := s.repo.SaveResource(&resource)
		if saveErr != nil {
			log.Printf("resource failure state write failed resource=%s: %v", resource.ID, saveErr)
		}
		return &resource, errors.Join(err, saveErr)
	}
	resource.Status = model.ResourceStatusReady
	resource.ETag = etag
	if err := s.repo.SaveResource(&resource); err != nil {
		return &resource, err
	}
	s.recordActivity(userID, "resource", 1)
	return &resource, nil
}

func (s *Service) writeResourceBody(userID string, resource *model.Resource, body io.Reader) (string, error) {
	if resource == nil {
		return "", errors.New("资源不存在")
	}
	if resource.Provider == "local" {
		filePath := filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
			return "", err
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(file, body)
		closeErr := file.Close()
		return "", errors.Join(copyErr, closeErr)
	}
	setting, err := s.ossSettingForResource(userID, resource)
	if err != nil {
		return "", err
	}
	return putOSSObject(setting, resource.ObjectKey, resource.MimeType, resource.Size, body)
}

func localObjectKey(userID string, kind string, fileName string, now time.Time) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	return path.Join("users", safeObjectSegment(userID), kind, now.Format("2006/01/02"), newID()+ext)
}

type remoteResourcePayload struct {
	url      string
	endpoint string
	fileName string
	mimeType string
	data     []byte
}

func downloadRemoteResource(rawURL string, maxBytes int64) (remoteResourcePayload, error) {
	parsed, err := validateRemoteResourceURL(rawURL)
	if err != nil {
		return remoteResourcePayload{}, err
	}
	client := OutboundHTTPClient(90 * time.Second)
	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return remoteResourcePayload{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return remoteResourcePayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remoteResourcePayload{}, fmt.Errorf("远程资源下载失败：%s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return remoteResourcePayload{}, err
	}
	if int64(len(data)) >= maxBytes {
		return remoteResourcePayload{}, BadAuthRequest(fmt.Sprintf("远程资源必须小于 %s", formatStorageLimit(maxBytes)))
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	fileName := path.Base(parsed.Path)
	if fileName == "" || fileName == "." || !strings.Contains(fileName, ".") {
		fileName = "resource." + extensionFromMimeType(mimeType)
	}
	return remoteResourcePayload{url: parsed.String(), endpoint: parsed.Host, fileName: fileName, mimeType: mimeType, data: data}, nil
}

func openRemoteResource(rawURL string) (io.ReadCloser, error) {
	parsed, err := validateRemoteResourceURL(rawURL)
	if err != nil {
		return nil, err
	}
	client := OutboundHTTPClient(90 * time.Second)
	resp, err := client.Get(parsed.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("远程资源读取失败：%s", resp.Status)
	}
	return resp.Body, nil
}

func validateRemoteResourceURL(rawURL string) (*url.URL, error) {
	return ValidateOutboundURL(rawURL)
}

func extensionFromMimeType(mimeType string) string {
	if strings.Contains(mimeType, "png") {
		return "png"
	}
	if strings.Contains(mimeType, "jpeg") {
		return "jpg"
	}
	if strings.Contains(mimeType, "webp") {
		return "webp"
	}
	if strings.Contains(mimeType, "gif") {
		return "gif"
	}
	if strings.Contains(mimeType, "mp4") {
		return "mp4"
	}
	if strings.Contains(mimeType, "webm") {
		return "webm"
	}
	if strings.Contains(mimeType, "mpeg") {
		return "mp3"
	}
	if strings.Contains(mimeType, "wav") {
		return "wav"
	}
	return "bin"
}

func imageDimensions(data []byte) (int, int) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func (s *Service) activeOSSSetting() (ossSettingValue, error) {
	_, setting, err := s.readOSSSetting()
	if err != nil {
		return ossSettingValue{}, err
	}
	return validateActiveOSSSetting(setting, "管理员尚未启用 OSS", "平台 OSS 配置不完整，请联系管理员")
}

func (s *Service) activeResourceOSSSetting(userID string) (ossSettingValue, string, bool, error) {
	userSetting, value, err := s.readUserOSSSetting(userID)
	if err != nil {
		return ossSettingValue{}, "", false, err
	}
	if userSetting != nil && value.Enabled {
		value, err = validateActiveOSSSetting(value, "用户 OSS 尚未启用", "你的 OSS 配置不完整")
		return value, userSetting.ID, true, err
	}
	_, systemValue, err := s.readOSSSetting()
	if err != nil {
		return ossSettingValue{}, "", false, err
	}
	if !systemValue.Enabled {
		return ossSettingValue{}, "", false, nil
	}
	systemValue, err = validateActiveOSSSetting(systemValue, "管理员尚未启用 OSS", "平台 OSS 配置不完整，请联系管理员")
	return systemValue, "", true, err
}

func (s *Service) ossSettingForResource(userID string, resource *model.Resource) (ossSettingValue, error) {
	var setting ossSettingValue
	var err error
	if resource.StorageSettingID != "" {
		_, setting, err = s.readUserOSSSettingByID(userID, resource.StorageSettingID)
	} else {
		_, setting, err = s.readOSSSetting()
	}
	if err != nil {
		return ossSettingValue{}, err
	}
	setting.Provider = firstNonEmpty(resource.Provider, setting.Provider)
	setting.Endpoint = firstNonEmpty(resource.Endpoint, setting.Endpoint)
	setting.Bucket = firstNonEmpty(resource.Bucket, setting.Bucket)
	if setting.AccessKeyID == "" || setting.AccessKeySecret == "" {
		return ossSettingValue{}, errors.New("OSS 访问密钥不可用")
	}
	return setting, nil
}

func validateActiveOSSSetting(setting ossSettingValue, disabledMessage string, incompleteMessage string) (ossSettingValue, error) {
	setting = normalizeOSSSetting(setting)
	if !setting.Enabled {
		return ossSettingValue{}, BadAuthRequest(disabledMessage)
	}
	if setting.Provider != "aliyun" {
		return ossSettingValue{}, BadAuthRequest("暂时只支持阿里云 OSS")
	}
	if setting.Bucket == "" || setting.Endpoint == "" || setting.AccessKeyID == "" || setting.AccessKeySecret == "" {
		return ossSettingValue{}, BadAuthRequest(incompleteMessage)
	}
	return setting, nil
}

func normalizeResourceKind(kind string, mimeType string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "image", "video", "audio", "file":
		return kind
	}
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if strings.HasPrefix(mimeType, "video/") {
		return "video"
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return "audio"
	}
	return "file"
}

func ossObjectKey(setting ossSettingValue, userID string, kind string, fileName string, now time.Time) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	name := newID()
	parts := []string{setting.PathPrefix, "users", safeObjectSegment(userID), kind, now.Format("2006/01/02"), name + ext}
	return strings.Trim(strings.Join(nonEmptySegments(parts), "/"), "/")
}

func putOSSObject(setting ossSettingValue, objectKey string, mimeType string, size int64, body io.Reader) (string, error) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	req, err := newOSSRequest(http.MethodPut, setting, objectKey, mimeType, body)
	if err != nil {
		return "", err
	}
	if size > 0 {
		req.ContentLength = size
	}
	resp, err := OutboundHTTPClient(2 * time.Minute).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("OSS 上传失败：%s %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

type ossObjectStream struct {
	body          io.ReadCloser
	statusCode    int
	contentLength int64
	contentRange  string
	acceptRanges  string
}

func getOSSObjectRange(ctx context.Context, setting ossSettingValue, objectKey string, rangeHeader string) (*ossObjectStream, error) {
	req, err := newOSSRequestWithContext(ctx, http.MethodGet, setting, objectKey, "", nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := OutboundHTTPClient(2 * time.Minute).Do(req)
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		defer resp.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OSS 读取失败：%s %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return &ossObjectStream{body: resp.Body, statusCode: resp.StatusCode, contentLength: resp.ContentLength, contentRange: resp.Header.Get("Content-Range"), acceptRanges: firstNonEmpty(resp.Header.Get("Accept-Ranges"), "bytes")}, nil
}

func normalizeSingleByteRange(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 || !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return ""
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok || (start == "" && end == "") || !decimalDigits(start) || !decimalDigits(end) {
		return ""
	}
	return "bytes=" + start + "-" + end
}

func decimalDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func signedOSSObjectURL(setting ossSettingValue, objectKey string, expiresAt time.Time) (string, error) {
	baseURL, err := ossBucketBaseURL(setting)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(setting.AccessKeyID) == "" || strings.TrimSpace(setting.AccessKeySecret) == "" {
		return "", errors.New("OSS 访问密钥不可用")
	}
	objectKey = strings.TrimLeft(strings.TrimSpace(objectKey), "/")
	if objectKey == "" {
		return "", errors.New("OSS 对象路径为空")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/" + escapeObjectKey(objectKey)
	expires := strconv.FormatInt(expiresAt.UTC().Unix(), 10)
	stringToSign := strings.Join([]string{http.MethodGet, "", "", expires, "/" + setting.Bucket + "/" + objectKey}, "\n")
	mac := hmac.New(sha1.New, []byte(setting.AccessKeySecret))
	_, _ = mac.Write([]byte(stringToSign))
	query := baseURL.Query()
	query.Set("OSSAccessKeyId", setting.AccessKeyID)
	query.Set("Expires", expires)
	query.Set("Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	baseURL.RawQuery = query.Encode()
	return baseURL.String(), nil
}

func newOSSRequest(method string, setting ossSettingValue, objectKey string, contentType string, body io.Reader) (*http.Request, error) {
	return newOSSRequestWithContext(context.Background(), method, setting, objectKey, contentType, body)
}

func newOSSRequestWithContext(ctx context.Context, method string, setting ossSettingValue, objectKey string, contentType string, body io.Reader) (*http.Request, error) {
	baseURL, err := ossBucketBaseURL(setting)
	if err != nil {
		return nil, err
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/" + escapeObjectKey(objectKey)
	req, err := http.NewRequestWithContext(ctx, method, baseURL.String(), body)
	if err != nil {
		return nil, err
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	stringToSign := strings.Join([]string{method, "", contentType, date, "/" + setting.Bucket + "/" + objectKey}, "\n")
	mac := hmac.New(sha1.New, []byte(setting.AccessKeySecret))
	_, _ = mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", "OSS "+setting.AccessKeyID+":"+signature)
	return req, nil
}

func ossBucketBaseURL(setting ossSettingValue) (*url.URL, error) {
	endpoint := strings.TrimRight(setting.Endpoint, "/")
	if endpoint == "" {
		return nil, errors.New("OSS Endpoint 为空")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if parsed.Host == "" {
		return nil, errors.New("OSS Endpoint 格式不正确")
	}
	if !strings.HasPrefix(parsed.Host, setting.Bucket+".") {
		parsed.Host = setting.Bucket + "." + parsed.Host
	}
	return parsed, nil
}

func escapeObjectKey(key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func safeObjectSegment(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	return strings.Trim(value, "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nonEmptySegments(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(path.Clean("/"+value)), "/")
		if value != "" && value != "." {
			result = append(result, value)
		}
	}
	return result
}

package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

type generatedResourceSource struct {
	TaskID        string
	Attempt       string
	Path          string
	ContentSHA256 string
	QuotaDay      string
}

type generatedMediaCandidate struct {
	item          map[string]interface{}
	raw           string
	path          string
	mimeType      string
	kind          string
	data          []byte
	width         int
	height        int
	durationMs    int64
	contentSHA256 string
	resource      *model.Resource
	quotaDay      string
	quotaReserved bool
	dailyReserved bool
}

type generatedMediaPersistenceOptions struct {
	source             generatedResourceSource
	skipInvalidDataURL bool
	enforceQuota       bool
	recoverPendingNow  bool
	continueCheck      func() error
}

const generatedResourcePendingStaleAfter = 3 * time.Minute

func (s *Service) persistGeneratedMediaResult(userID string, result map[string]interface{}) (map[string]interface{}, error) {
	return s.persistGeneratedMediaResultMode(userID, result, generatedMediaPersistenceOptions{enforceQuota: true})
}

func (s *Service) persistTaskGeneratedMediaResult(task model.Task, result map[string]interface{}) (map[string]interface{}, error) {
	return s.persistTaskGeneratedMediaResultMode(task, result, false, nil)
}

func (s *Service) persistRecoveringTaskGeneratedMediaResult(task model.Task, result map[string]interface{}) (map[string]interface{}, error) {
	return s.persistRecoveringTaskGeneratedMediaResultWithCheck(task, result, nil)
}

func (s *Service) persistRecoveringTaskGeneratedMediaResultWithCheck(task model.Task, result map[string]interface{}, continueCheck func() error) (map[string]interface{}, error) {
	return s.persistTaskGeneratedMediaResultMode(task, result, true, continueCheck)
}

func (s *Service) persistTaskGeneratedMediaResultMode(task model.Task, result map[string]interface{}, recoverPendingNow bool, continueCheck func() error) (map[string]interface{}, error) {
	attempt := strings.TrimSpace(task.BillingOrderID)
	if attempt != "" {
		attempt = "billing:" + attempt
	} else {
		attemptNumber := task.Attempts
		if attemptNumber < 1 {
			attemptNumber = 1
		}
		attempt = "attempt:" + strconv.Itoa(attemptNumber)
	}
	return s.persistGeneratedMediaResultMode(task.UserID, result, generatedMediaPersistenceOptions{
		source:            generatedResourceSource{TaskID: task.ID, Attempt: attempt},
		enforceQuota:      true,
		recoverPendingNow: recoverPendingNow,
		continueCheck:     continueCheck,
	})
}

func (s *Service) persistLegacyGeneratedMediaResult(userID string, result map[string]interface{}) (map[string]interface{}, error) {
	return s.persistGeneratedMediaResultMode(userID, result, generatedMediaPersistenceOptions{skipInvalidDataURL: true})
}

func (s *Service) persistGeneratedMediaResultMode(userID string, result map[string]interface{}, options generatedMediaPersistenceOptions) (map[string]interface{}, error) {
	if result == nil {
		return map[string]interface{}{}, nil
	}
	value, err := s.persistGeneratedMediaValueMode(userID, result, options)
	if value == nil {
		return nil, err
	}
	persisted, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("生成结果不是对象")
	}
	return persisted, err
}

func (s *Service) persistGeneratedMediaValue(userID string, value interface{}) (interface{}, error) {
	return s.persistGeneratedMediaValueMode(userID, value, generatedMediaPersistenceOptions{enforceQuota: true})
}

// 生成媒体先完整解码、校验并预留全部额度，再开始任何文件写入；后续条目无效时不能留下前半批孤儿资源。
func (s *Service) persistGeneratedMediaValueMode(userID string, value interface{}, options generatedMediaPersistenceOptions) (interface{}, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized interface{}
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}

	candidates := make([]*generatedMediaCandidate, 0)
	if err := s.collectGeneratedMediaCandidates(normalized, "$", options.skipInvalidDataURL, &candidates); err != nil {
		return normalized, err
	}
	quotaDays := make(map[string]string)
	for _, candidate := range candidates {
		if checkErr := checkGeneratedMediaContinuation(options); checkErr != nil {
			applyReadyGeneratedResources(candidates)
			return normalized, checkErr
		}
		resource, err := s.generatedResourceForCandidate(userID, options.source, candidate, options.recoverPendingNow)
		if err != nil {
			applyReadyGeneratedResources(candidates)
			return normalized, err
		}
		candidate.resource = resource
	}
	if options.enforceQuota {
		for index, candidate := range candidates {
			if checkErr := checkGeneratedMediaContinuation(options); checkErr != nil {
				s.releaseGeneratedCandidateQuotas(userID, candidates[:index])
				applyReadyGeneratedResources(candidates)
				return normalized, checkErr
			}
			if candidate.resource != nil && candidate.resource.Status == model.ResourceStatusReady {
				continue
			}
			if candidate.resource != nil && candidate.resource.QuotaDay != "" {
				candidate.quotaDay = candidate.resource.QuotaDay
				err = s.reserveRecoveredGeneratedResourceStorage(userID, int64(len(candidate.data)))
			} else {
				candidate.quotaDay, err = s.reserveGeneratedResourceQuota(userID, int64(len(candidate.data)))
				candidate.dailyReserved = err == nil
			}
			if err != nil {
				s.releaseGeneratedCandidateQuotas(userID, candidates[:index])
				applyReadyGeneratedResources(candidates)
				return normalized, err
			}
			candidate.quotaReserved = true
		}
	}
	for _, candidate := range candidates {
		if checkErr := checkGeneratedMediaContinuation(options); checkErr != nil {
			s.releaseGeneratedCandidateQuotas(userID, candidates)
			applyReadyGeneratedResources(candidates)
			return normalized, checkErr
		}
		if candidate.resource == nil || candidate.resource.Status != model.ResourceStatusPending {
			continue
		}
		updatedBefore := time.Now().Add(-generatedResourcePendingStaleAfter)
		if options.recoverPendingNow {
			updatedBefore = time.Now()
		}
		claimed, claimErr := s.repo.ClaimStaleGeneratedResource(candidate.resource.ID, userID, updatedBefore)
		if claimErr != nil || !claimed {
			s.releaseGeneratedCandidateQuotas(userID, candidates)
			applyReadyGeneratedResources(candidates)
			if claimErr != nil {
				return normalized, claimErr
			}
			return normalized, fmt.Errorf("同一任务结果路径 %s 正由另一个 Worker 恢复，请稍后刷新", candidate.path)
		}
		candidate.resource.UpdatedAt = time.Now()
		candidate.resource.Error = ""
		if candidate.resource.QuotaDay == "" {
			quotaDays[candidate.resource.ID] = candidate.quotaDay
		}
	}
	if saveErr := s.repo.SetClaimedGeneratedResourceQuotaDays(userID, quotaDays); saveErr != nil {
		s.releaseGeneratedCandidateQuotas(userID, candidates)
		applyReadyGeneratedResources(candidates)
		return normalized, saveErr
	}
	for _, candidate := range candidates {
		if candidate.resource != nil && candidate.resource.Status == model.ResourceStatusPending && candidate.resource.QuotaDay == "" {
			candidate.resource.QuotaDay = candidate.quotaDay
			// quota_day 已原子落库；后续批次中断也保留这笔日额度，下一次恢复不得重复预留。
			candidate.dailyReserved = false
		}
	}

	for index, candidate := range candidates {
		if checkErr := checkGeneratedMediaContinuation(options); checkErr != nil {
			s.releaseGeneratedCandidateQuotas(userID, candidates[index:])
			applyReadyGeneratedResources(candidates)
			return normalized, checkErr
		}
		if candidate.resource != nil && candidate.resource.Status == model.ResourceStatusPending {
			persisted, recoverErr := s.writeClaimedPendingGeneratedResource(userID, candidate.resource, candidate)
			if candidate.quotaReserved {
				// pending 行继续持有原有或本次预留的当日额度；这里只释放进程内存储预留。
				s.commitUserUploadQuota(userID, int64(len(candidate.data)))
				candidate.quotaReserved = false
			}
			if recoverErr != nil {
				s.releaseGeneratedCandidateQuotas(userID, candidates[index+1:])
				applyReadyGeneratedResources(candidates)
				return normalized, fmt.Errorf("生成资源恢复写入失败：%w", recoverErr)
			}
			candidate.resource = persisted
			continue
		}
		if candidate.resource == nil {
			storedNow := false
			storedResource, storeErr := s.storeResourceWithSource(
				userID,
				candidate.kind,
				"generated."+extensionFromMimeType(candidate.mimeType),
				candidate.mimeType,
				int64(len(candidate.data)),
				candidate.width,
				candidate.height,
				candidate.durationMs,
				bytes.NewReader(candidate.data),
				generatedResourceSource{TaskID: options.source.TaskID, Attempt: options.source.Attempt, Path: candidate.path, ContentSHA256: candidate.contentSHA256, QuotaDay: candidate.quotaDay},
			)
			storedNow = storeErr == nil
			if storeErr == nil {
				candidate.resource = storedResource
			} else {
				// 唯一索引竞争时只允许复用已经完整落盘且摘要一致的资源；pending 结果不能伪装完成。
				if raced, lookupErr := s.generatedResourceForCandidate(userID, options.source, candidate, options.recoverPendingNow); lookupErr == nil && raced != nil && raced.Status == model.ResourceStatusReady {
					candidate.resource = raced
					storeErr = nil
					storedNow = false
				}
			}
			if storeErr != nil {
				if candidate.quotaReserved {
					if storedResource != nil && options.source.TaskID != "" && (storedResource.Status == model.ResourceStatusPending || storedResource.Status == model.ResourceStatusReady) {
						// 任务生成资源保留 pending 行和当日额度，供“恢复结果”覆写同一对象键。
						s.commitUserUploadQuota(userID, int64(len(candidate.data)))
					} else {
						s.releaseUserUploadQuota(userID, candidate.quotaDay, int64(len(candidate.data)))
					}
					candidate.quotaReserved = false
					candidate.dailyReserved = false
				}
				s.releaseGeneratedCandidateQuotas(userID, candidates[index+1:])
				applyReadyGeneratedResources(candidates)
				return normalized, fmt.Errorf("生成内容写入资源存储失败：%w", storeErr)
			}
			if candidate.quotaReserved {
				if storedNow {
					s.commitUserUploadQuota(userID, int64(len(candidate.data)))
				} else {
					// 并发调用已经先写好同一资源，本次预留不能重复计入每日上传量。
					s.releaseUserUploadQuota(userID, candidate.quotaDay, int64(len(candidate.data)))
				}
				candidate.quotaReserved = false
				candidate.dailyReserved = false
			}
		}
	}
	applyReadyGeneratedResources(candidates)
	if checkErr := checkGeneratedMediaContinuation(options); checkErr != nil {
		return normalized, checkErr
	}
	return normalized, nil
}

func checkGeneratedMediaContinuation(options generatedMediaPersistenceOptions) error {
	if options.continueCheck == nil {
		return nil
	}
	return options.continueCheck()
}

func (s *Service) collectGeneratedMediaCandidates(value interface{}, sourcePath string, skipInvalidDataURL bool, candidates *[]*generatedMediaCandidate) error {
	switch item := value.(type) {
	case []interface{}:
		for index, child := range item {
			if err := s.collectGeneratedMediaCandidates(child, sourcePath+"/"+strconv.Itoa(index), skipInvalidDataURL, candidates); err != nil {
				return err
			}
		}
	case map[string]interface{}:
		if raw := inlineMediaValue(item); raw != "" {
			mimeType, data, err := s.decodeDataURL(raw)
			if err != nil && !skipInvalidDataURL {
				return err
			}
			if err == nil {
				kind := normalizeResourceKind("", mimeType)
				width, height := intValue(item["width"]), intValue(item["height"])
				if kind == "image" && (width <= 0 || height <= 0) {
					width, height = imageDimensions(data)
				}
				digest := sha256.Sum256(data)
				*candidates = append(*candidates, &generatedMediaCandidate{
					item: item, raw: raw, path: sourcePath, mimeType: mimeType, kind: kind, data: data,
					width: width, height: height, durationMs: int64(intValue(item["durationMs"])), contentSHA256: hex.EncodeToString(digest[:]),
				})
			}
		}
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := s.collectGeneratedMediaCandidates(item[key], sourcePath+"/"+escapeGeneratedResourcePath(key), skipInvalidDataURL, candidates); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) generatedResourceForCandidate(userID string, source generatedResourceSource, candidate *generatedMediaCandidate, recoverPendingNow bool) (*model.Resource, error) {
	if source.TaskID == "" || source.Attempt == "" || candidate == nil {
		return nil, nil
	}
	resource, err := s.repo.GeneratedResourceForTaskOutput(userID, source.TaskID, source.Attempt, candidate.path)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if resource.ContentSHA256 != candidate.contentSHA256 || resource.Size != int64(len(candidate.data)) || !strings.EqualFold(resource.MimeType, candidate.mimeType) {
		return nil, fmt.Errorf("同一任务结果路径 %s 返回了不同媒体内容，已拒绝覆盖原资源", candidate.path)
	}
	if resource.Status == model.ResourceStatusReady {
		return resource, nil
	}
	if !recoverPendingNow && time.Since(resource.UpdatedAt) < generatedResourcePendingStaleAfter {
		return nil, fmt.Errorf("同一任务结果路径 %s 的资源仍在持久化，请稍后核对任务详情，不要重新调用供应商", candidate.path)
	}
	return resource, nil
}

func (s *Service) writeClaimedPendingGeneratedResource(userID string, resource *model.Resource, candidate *generatedMediaCandidate) (*model.Resource, error) {
	if resource == nil || candidate == nil {
		return nil, errors.New("待恢复生成资源无效")
	}
	etag, writeErr := s.writeResourceBody(userID, resource, bytes.NewReader(candidate.data))
	resource.UpdatedAt = time.Now()
	if writeErr != nil {
		resource.Status = model.ResourceStatusPending
		resource.Error = "任务生成资源恢复写入中断，可再次从任务详情恢复"
		logErr := s.repo.SaveResource(resource)
		return nil, errors.Join(writeErr, logErr)
	}
	resource.Status = model.ResourceStatusReady
	resource.ETag = etag
	resource.Error = ""
	if err := s.repo.SaveResource(resource); err != nil {
		// 对象已经完整写入且当日额度已计入；数据库仍保留 pending 行供下一次覆写同一对象键。
		resource.Status = model.ResourceStatusPending
		resource.Error = "任务生成资源状态保存中断，可再次从任务详情恢复"
		return nil, err
	}
	s.recordActivity(userID, "resource", 1)
	return resource, nil
}

func (s *Service) releaseGeneratedCandidateQuotas(userID string, candidates []*generatedMediaCandidate) {
	for _, candidate := range candidates {
		if candidate == nil || !candidate.quotaReserved {
			continue
		}
		if candidate.dailyReserved {
			s.releaseUserUploadQuota(userID, candidate.quotaDay, int64(len(candidate.data)))
		} else {
			s.commitUserUploadQuota(userID, int64(len(candidate.data)))
		}
		candidate.quotaReserved = false
		candidate.dailyReserved = false
	}
}

func applyReadyGeneratedResources(candidates []*generatedMediaCandidate) {
	for _, candidate := range candidates {
		if candidate == nil || candidate.resource == nil || candidate.resource.Status != model.ResourceStatusReady {
			continue
		}
		applyGeneratedResource(candidate.item, candidate.raw, candidate.resource)
	}
}

func applyGeneratedResource(item map[string]interface{}, raw string, resource *model.Resource) {
	if item == nil || resource == nil {
		return
	}
	resourceURL := "/api/resources/" + resource.ID + "/file"
	for _, key := range []string{"dataUrl", "content", "url", "coverUrl"} {
		if text, ok := item[key].(string); ok && (text == raw || strings.HasPrefix(text, "blob:")) {
			item[key] = resourceURL
		}
	}
	if _, ok := item["dataUrl"]; ok {
		item["dataUrl"] = resourceURL
	}
	item["url"] = resourceURL
	item["storageKey"] = "resource:" + resource.ID
	item["resourceId"] = resource.ID
	item["bytes"] = resource.Size
	item["mimeType"] = resource.MimeType
	item["width"] = resource.Width
	item["height"] = resource.Height
}

func escapeGeneratedResourcePath(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func inlineMediaValue(item map[string]interface{}) string {
	for _, key := range []string{"dataUrl", "content", "url", "coverUrl"} {
		if text, ok := item[key].(string); ok && (strings.HasPrefix(text, "data:image/") || strings.HasPrefix(text, "data:video/") || strings.HasPrefix(text, "data:audio/")) {
			return text
		}
	}
	return ""
}

func (s *Service) decodeDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", nil, fmt.Errorf("%w：格式无效", errInvalidGeneratedDataURL)
	}
	mimeType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("%w：base64 解码失败：%v", errInvalidGeneratedDataURL, err)
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > megabytes(policy.Resource.GeneratedFileMB) {
		return "", nil, fmt.Errorf("单个生成资源超过 %dMB", policy.Resource.GeneratedFileMB)
	}
	return mimeType, data, nil
}

func intValue(value interface{}) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	default:
		return 0
	}
}

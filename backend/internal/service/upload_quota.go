package service

import (
	"errors"
	"fmt"
	"log"
	"time"

	"infinite-canvas/backend/internal/repository"
)

// 上传额度在写文件或 OSS 前原子预留，避免并发请求同时通过日限额检查。
func (s *Service) reserveUserUploadQuota(userID string, size int64) (string, error) {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return "", err
	}
	return s.reserveUserStoredFileQuota(userID, size, megabytes(policy.Resource.ResourceUploadMB), megabytes(policy.Resource.DailyUploadMB), gigabytes(policy.Resource.StoredFileGB), fmt.Sprintf("单个上传文件必须小于 %dMB", policy.Resource.ResourceUploadMB))
}

func (s *Service) reserveSessionUploadQuota(userID string, size int64) (string, error) {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return "", err
	}
	return s.reserveUserStoredFileQuota(userID, size, megabytes(policy.Resource.SessionUploadMB)+1, megabytes(policy.Resource.DailyUploadMB), gigabytes(policy.Resource.StoredFileGB), fmt.Sprintf("会话文件不能超过 %dMB", policy.Resource.SessionUploadMB))
}

func (s *Service) reserveGeneratedResourceQuota(userID string, size int64) (string, error) {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return "", err
	}
	return s.reserveUserStoredFileQuota(userID, size, megabytes(policy.Resource.GeneratedFileMB)+1, megabytes(policy.Resource.DailyUploadMB), gigabytes(policy.Resource.StoredFileGB), fmt.Sprintf("单个生成文件不能超过 %dMB", policy.Resource.GeneratedFileMB))
}

func (s *Service) reserveUserStoredFileQuota(userID string, size int64, exclusiveSingleFileLimit int64, dailyLimit int64, storedLimit int64, singleFileMessage string) (string, error) {
	if size <= 0 {
		return "", BadAuthRequest("上传文件不能为空")
	}
	if size >= exclusiveSingleFileLimit {
		return "", BadAuthRequest(singleFileMessage)
	}
	day := time.Now().UTC().Format("2006-01-02")
	unlockStorage := s.lockUserStorage(userID)
	defer unlockStorage()
	storedBytes, err := s.repo.UserStoredFileBytes(userID)
	if err != nil {
		return "", err
	}
	if storedBytes+s.pendingStorageBytes(userID)+size >= storedLimit {
		return "", BadAuthRequest(fmt.Sprintf("账号资源和会话附件已达到 %s 上限，请联系管理员清理历史文件", formatStorageLimit(storedLimit)))
	}
	s.addPendingStorage(userID, size)
	if err := s.repo.ReserveDailyUpload(userID, day, size, dailyLimit); err != nil {
		s.decreasePendingStorage(userID, size)
		if errors.Is(err, repository.ErrDailyUploadLimitExceeded) {
			return "", BadAuthRequest(fmt.Sprintf("每个账号 UTC 自然日上传总量必须小于 %s", formatStorageLimit(dailyLimit)))
		}
		return "", err
	}
	return day, nil
}

func formatStorageLimit(value int64) string {
	if value%(1<<30) == 0 {
		return fmt.Sprintf("%dGB", value>>30)
	}
	return fmt.Sprintf("%dMB", value>>20)
}

func (s *Service) releaseUserUploadQuota(userID string, day string, size int64) {
	if day == "" || size <= 0 {
		return
	}
	unlockStorage := s.lockUserStorage(userID)
	defer unlockStorage()
	s.decreasePendingStorage(userID, size)
	if err := s.repo.ReleaseDailyUpload(userID, day, size); err != nil {
		log.Printf("release upload quota failed: user=%s day=%s size=%d error=%v", userID, day, size, err)
	}
}

func (s *Service) commitUserUploadQuota(userID string, size int64) {
	if size <= 0 {
		return
	}
	unlockStorage := s.lockUserStorage(userID)
	defer unlockStorage()
	s.decreasePendingStorage(userID, size)
}

// 崩溃恢复的 pending 资源已经计入当日上传量，只重新占用并发中的账号存储预留，避免重复累计日额度。
func (s *Service) reserveRecoveredGeneratedResourceStorage(userID string, size int64) error {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return err
	}
	if size <= 0 || size > megabytes(policy.Resource.GeneratedFileMB) {
		return BadAuthRequest(fmt.Sprintf("单个生成文件不能超过 %dMB", policy.Resource.GeneratedFileMB))
	}
	unlockStorage := s.lockUserStorage(userID)
	defer unlockStorage()
	storedBytes, err := s.repo.UserStoredFileBytes(userID)
	if err != nil {
		return err
	}
	storedLimit := gigabytes(policy.Resource.StoredFileGB)
	if storedBytes+s.pendingStorageBytes(userID)+size >= storedLimit {
		return BadAuthRequest(fmt.Sprintf("账号资源和会话附件已达到 %s 上限，请联系管理员清理历史文件", formatStorageLimit(storedLimit)))
	}
	s.addPendingStorage(userID, size)
	return nil
}

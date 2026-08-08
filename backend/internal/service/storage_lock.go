package service

import "sync"

type userStorageLock struct {
	mu   sync.Mutex
	refs int
}

// 配额只需串行同一账号；锁表引用计数归零即回收，避免活跃用户持续占用内存。
func (s *Service) lockUserStorage(userID string) func() {
	s.storageMu.Lock()
	if s.storageLocks == nil {
		s.storageLocks = make(map[string]*userStorageLock)
	}
	lock := s.storageLocks[userID]
	if lock == nil {
		lock = &userStorageLock{}
		s.storageLocks[userID] = lock
	}
	lock.refs++
	s.storageMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.storageMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.storageLocks, userID)
		}
		s.storageMu.Unlock()
	}
}

func (s *Service) pendingStorageBytes(userID string) int64 {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	return s.pendingStorage[userID]
}

func (s *Service) addPendingStorage(userID string, size int64) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.pendingStorage == nil {
		s.pendingStorage = make(map[string]int64)
	}
	s.pendingStorage[userID] += size
}

func (s *Service) decreasePendingStorage(userID string, size int64) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	remaining := s.pendingStorage[userID] - size
	if remaining > 0 {
		s.pendingStorage[userID] = remaining
		return
	}
	delete(s.pendingStorage, userID)
}

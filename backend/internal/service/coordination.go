package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/model"

	"github.com/redis/go-redis/v9"
)

type localRateEntry struct {
	expiresAt time.Time
	count     int
}

const (
	minChannelConcurrencyLimit     = 1
	maxChannelConcurrencyLimit     = maxRuntimeConcurrency
	defaultChannelConcurrencyValue = 3
	localCoordinatorSweepInterval  = time.Minute
)

type channelSlotError struct {
	scope string
	limit int
	err   error
}

func (e channelSlotError) Error() string {
	if errors.Is(e.err, context.DeadlineExceeded) {
		return fmt.Sprintf("等待渠道并发槽位超时（渠道 %s，并发上限 %d）", e.scope, e.limit)
	}
	if errors.Is(e.err, context.Canceled) {
		return fmt.Sprintf("等待渠道并发槽位已取消（渠道 %s，并发上限 %d）", e.scope, e.limit)
	}
	return fmt.Sprintf("获取渠道并发配额失败（渠道 %s，并发上限 %d）；本次尚未调用供应商，请联系管理员检查运行时协调器", e.scope, e.limit)
}

func (e channelSlotError) Unwrap() error { return e.err }

func ChannelSlotFailureDetails(err error) (string, string) {
	var slotErr channelSlotError
	if !errors.As(err, &slotErr) {
		return "", ""
	}
	if errors.Is(slotErr, context.DeadlineExceeded) {
		return "channel_concurrency_wait_timeout", slotErr.Error()
	}
	if errors.Is(slotErr, context.Canceled) {
		return "channel_concurrency_wait_cancelled", slotErr.Error()
	}
	return "channel_concurrency_unavailable", slotErr.Error()
}

type runtimeCoordinator struct {
	redis      *redis.Client
	instanceID string
	localMu    sync.Mutex
	localRate  map[string]localRateEntry
	localSlots map[string]map[string]time.Time
	localSweep time.Time
}

var fixedWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return count
`)

var acquireSlotScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
local current_ttl = redis.call('PTTL', KEYS[1])
if current_ttl < tonumber(ARGV[5]) then redis.call('PEXPIRE', KEYS[1], ARGV[5]) end
return 1
`)

var renewSlotScript = redis.NewScript(`
local current = redis.call('ZSCORE', KEYS[1], ARGV[2])
if not current then return 0 end
if tonumber(current) <= tonumber(ARGV[1]) then
  redis.call('ZREM', KEYS[1], ARGV[2])
  return 0
end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[2])
local current_ttl = redis.call('PTTL', KEYS[1])
if current_ttl < tonumber(ARGV[4]) then redis.call('PEXPIRE', KEYS[1], ARGV[4]) end
return 1
`)

var errRuntimeSlotLeaseLost = errors.New("运行时并发槽位租约已失效")

type runtimeSlotLease struct {
	coordinator *runtimeCoordinator
	scope       string
	token       string
}

func (l *runtimeSlotLease) Renew(ctx context.Context, ttl time.Duration) error {
	if l == nil || l.coordinator == nil {
		return errRuntimeSlotLeaseLost
	}
	return l.coordinator.renewSlot(ctx, l.scope, l.token, ttl)
}

func (l *runtimeSlotLease) Release() {
	if l == nil || l.coordinator == nil {
		return
	}
	l.coordinator.releaseSlot(l.scope, l.token)
}

func newRuntimeCoordinator(dialect string) (*runtimeCoordinator, error) {
	coordinator := &runtimeCoordinator{instanceID: newID(), localRate: map[string]localRateEntry{}, localSlots: map[string]map[string]time.Time{}}
	redisURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if redisURL == "" {
		if dialect == "postgres" {
			return coordinator, errors.New("PostgreSQL 多实例模式必须配置 REDIS_URL，用于限流、并发和熔断协调")
		}
		return coordinator, nil
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return coordinator, fmt.Errorf("REDIS_URL 无效：%w", err)
	}
	coordinator.redis = redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := coordinator.redis.Ping(ctx).Err(); err != nil {
		return coordinator, fmt.Errorf("Redis 不可用：%w", err)
	}
	return coordinator, nil
}

func (c *runtimeCoordinator) health(ctx context.Context) error {
	if c == nil {
		return errors.New("运行时协调器未初始化")
	}
	if c.redis == nil {
		return nil
	}
	return c.redis.Ping(ctx).Err()
}

func (c *runtimeCoordinator) allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if c.redis != nil {
		count, err := fixedWindowScript.Run(ctx, c.redis, []string{"canvas:rate:" + key}, window.Milliseconds()).Int64()
		return count <= int64(limit), err
	}
	c.localMu.Lock()
	defer c.localMu.Unlock()
	now := time.Now()
	c.sweepLocalStateLocked(now)
	entry := c.localRate[key]
	if !entry.expiresAt.After(now) {
		c.localRate[key] = localRateEntry{expiresAt: now.Add(window), count: 1}
		return true, nil
	}
	if entry.count >= limit {
		return false, nil
	}
	entry.count++
	c.localRate[key] = entry
	return true, nil
}

func (c *runtimeCoordinator) acquire(ctx context.Context, scope string, limit int, ttl time.Duration) (func(), bool, error) {
	lease, acquired, err := c.acquireLease(ctx, scope, limit, ttl)
	if err != nil || !acquired {
		return nil, false, err
	}
	return lease.Release, true, nil
}

// 长任务必须定期续租槽位；单纯使用很长 TTL 会让崩溃实例在 Redis 中长期占满 Worker。
func (c *runtimeCoordinator) acquireLease(ctx context.Context, scope string, limit int, ttl time.Duration) (*runtimeSlotLease, bool, error) {
	if c.redis == nil {
		c.localMu.Lock()
		now := time.Now()
		c.sweepLocalStateLocked(now)
		slots := c.localSlots[scope]
		if slots == nil {
			slots = map[string]time.Time{}
			c.localSlots[scope] = slots
		}
		for token, expiresAt := range slots {
			if !expiresAt.After(now) {
				delete(slots, token)
			}
		}
		if len(slots) >= limit {
			c.localMu.Unlock()
			return nil, false, nil
		}
		token := c.instanceID + ":" + newID()
		slots[token] = now.Add(ttl)
		c.localMu.Unlock()
		return &runtimeSlotLease{coordinator: c, scope: scope, token: token}, true, nil
	}
	// 有过期分数的有序集合避免实例崩溃后永久占槽，业务数据库仍保存任务与账本真相。
	key := "canvas:slots:" + scope
	token := c.instanceID + ":" + newID()
	now := time.Now()
	ok, err := acquireSlotScript.Run(ctx, c.redis, []string{key}, now.UnixMilli(), now.Add(ttl).UnixMilli(), limit, token, (ttl + time.Minute).Milliseconds()).Int()
	if err != nil || ok != 1 {
		return nil, false, err
	}
	return &runtimeSlotLease{coordinator: c, scope: scope, token: token}, true, nil
}

func (c *runtimeCoordinator) renewSlot(ctx context.Context, scope string, token string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("运行时并发槽位续租时长必须大于零")
	}
	now := time.Now()
	if c.redis == nil {
		c.localMu.Lock()
		defer c.localMu.Unlock()
		slots := c.localSlots[scope]
		expiresAt, exists := slots[token]
		if !exists || !expiresAt.After(now) {
			if exists {
				delete(slots, token)
			}
			if len(slots) == 0 {
				delete(c.localSlots, scope)
			}
			return errRuntimeSlotLeaseLost
		}
		slots[token] = now.Add(ttl)
		return nil
	}
	key := "canvas:slots:" + scope
	ok, err := renewSlotScript.Run(ctx, c.redis, []string{key}, now.UnixMilli(), token, now.Add(ttl).UnixMilli(), (ttl + time.Minute).Milliseconds()).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errRuntimeSlotLeaseLost
	}
	return nil
}

func (c *runtimeCoordinator) releaseSlot(scope string, token string) {
	if c.redis == nil {
		c.localMu.Lock()
		if active := c.localSlots[scope]; active != nil {
			delete(active, token)
			if len(active) == 0 {
				delete(c.localSlots, scope)
			}
		}
		c.localMu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.redis.ZRem(ctx, "canvas:slots:"+scope, token).Err(); err != nil {
		log.Printf("runtime slot release failed scope=%s: %v", scope, err)
	}
}

// SQLite 单实例模式同样会接触大量 IP、账号和临时会话键；定期回收已经到期的窗口与空槽位，避免常驻内存随历史请求单调增长。
func (c *runtimeCoordinator) sweepLocalStateLocked(now time.Time) {
	if !c.localSweep.IsZero() && now.Sub(c.localSweep) < localCoordinatorSweepInterval {
		return
	}
	for key, entry := range c.localRate {
		if !entry.expiresAt.After(now) {
			delete(c.localRate, key)
		}
	}
	for scope, slots := range c.localSlots {
		for token, expiresAt := range slots {
			if !expiresAt.After(now) {
				delete(slots, token)
			}
		}
		if len(slots) == 0 {
			delete(c.localSlots, scope)
		}
	}
	c.localSweep = now
}

func (c *runtimeCoordinator) acquireWithWait(ctx context.Context, scope string, limit int, ttl time.Duration) (func(), error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, acquired, err := c.acquire(ctx, scope, limit, ttl)
		if err != nil {
			return nil, err
		}
		if acquired {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *runtimeCoordinator) circuitOpen(ctx context.Context, channelID string) (bool, error) {
	if c.redis == nil || strings.TrimSpace(channelID) == "" {
		return false, nil
	}
	count, err := c.redis.Exists(ctx, "canvas:circuit:open:"+channelID).Result()
	return count > 0, err
}

func (c *runtimeCoordinator) recordChannelResult(ctx context.Context, channelID string, failed bool, failureLimit int, openDuration time.Duration) error {
	if c.redis == nil || strings.TrimSpace(channelID) == "" {
		return nil
	}
	failureKey := "canvas:circuit:failures:" + channelID
	openKey := "canvas:circuit:open:" + channelID
	if !failed {
		return c.redis.Del(ctx, failureKey, openKey).Err()
	}
	count, err := c.redis.Incr(ctx, failureKey).Result()
	if err != nil {
		return err
	}
	expireErr := c.redis.Expire(ctx, failureKey, time.Minute).Err()
	var openErr error
	if count >= int64(failureLimit) {
		openErr = c.redis.Set(ctx, openKey, "1", openDuration).Err()
	}
	return errors.Join(expireErr, openErr)
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func defaultChannelConcurrencyLimit() int {
	return effectiveChannelConcurrencyLimit(envInt("CANVAS_CHANNEL_CONCURRENCY", defaultChannelConcurrencyValue))
}

func effectiveChannelConcurrencyLimit(configured int) int {
	if configured < minChannelConcurrencyLimit || configured > maxChannelConcurrencyLimit {
		return defaultChannelConcurrencyValue
	}
	return configured
}

func (s *Service) AcquireChannelSlot(ctx context.Context, channelID string, fallbackScope string, ttl time.Duration) (func(), int, error) {
	setting, err := s.runtimeConcurrencySetting()
	limit := defaultChannelConcurrencyLimit()
	if err != nil {
		return nil, limit, channelSlotError{scope: firstNonEmpty(strings.TrimSpace(channelID), strings.TrimSpace(fallbackScope), "unknown"), limit: limit, err: fmt.Errorf("读取全局并发配置失败：%w", err)}
	}
	limit = setting.ChannelConcurrency
	scope := strings.TrimSpace(channelID)
	if scope != "" {
		var channel *model.ModelChannel
		metadata, _ := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
		if metadata.RequestKind == "health_check" {
			// 管理员需要在启用渠道前完成测活；仅探针允许读取停用渠道的并发配置，普通任务仍要求渠道已启用。
			channel, err = s.repo.AdminSystemChannel(scope)
		} else {
			channel, err = s.repo.SystemChannel(scope)
		}
		if err != nil {
			return nil, limit, channelSlotError{scope: scope, limit: limit, err: fmt.Errorf("读取渠道并发配置失败：%w", err)}
		}
		if channel.ConcurrencyLimit > 0 {
			if channel.ConcurrencyLimit < minChannelConcurrencyLimit || channel.ConcurrencyLimit > maxChannelConcurrencyLimit {
				return nil, limit, channelSlotError{scope: scope, limit: limit, err: errors.New("渠道并发配置超出 1-999 范围")}
			}
			limit = channel.ConcurrencyLimit
		}
	} else {
		scope = strings.TrimSpace(fallbackScope)
	}
	if scope == "" {
		return nil, limit, channelSlotError{scope: "unknown", limit: limit, err: errors.New("渠道并发范围为空")}
	}
	if s.coordinator == nil {
		return nil, limit, channelSlotError{scope: scope, limit: limit, err: errors.New("运行时协调器未初始化")}
	}
	release, err := s.coordinator.acquireWithWait(ctx, "channel:"+scope, limit, ttl)
	if err != nil {
		return nil, limit, channelSlotError{scope: scope, limit: limit, err: err}
	}
	return release, limit, nil
}

func (s *Service) ValidateRuntime() error {
	return s.runtimeErr
}

func (s *Service) AllowRequest(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if s.coordinator == nil {
		return false, errors.New("运行时协调器未初始化")
	}
	return s.coordinator.allow(ctx, key, limit, window)
}

func (s *Service) AcquireCustomRelaySlot(ctx context.Context, userID string, limit int, ttl time.Duration) (func(), bool, error) {
	if s.coordinator == nil {
		return nil, false, errors.New("运行时协调器未初始化")
	}
	return s.coordinator.acquire(ctx, "custom-relay:"+userID, limit, ttl)
}

func (s *Service) RecordChannelResult(ctx context.Context, channelID string, failed bool) error {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return err
	}
	if s.coordinator == nil {
		return errors.New("运行时协调器未初始化")
	}
	return s.coordinator.recordChannelResult(ctx, channelID, failed, policy.Request.ChannelCircuitFailureCount, time.Duration(policy.Request.ChannelCircuitOpenSeconds)*time.Second)
}

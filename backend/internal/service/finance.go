package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/gorm"
)

const CreditScale int64 = 1_000_000

const (
	proxyBillingIdempotencyPrefix    = "proxy:"
	proxyBillingIdempotencyMaxLength = 120
)

type WalletSummary struct {
	Account model.CreditAccount       `json:"account"`
	Entries []model.CreditLedgerEntry `json:"entries"`
	Total   int64                     `json:"total"`
	Page    int                       `json:"page"`
	Limit   int                       `json:"limit"`
	Policy  PublicCreditPolicy        `json:"policy"`
}

type RedeemBatchPage struct {
	Batches []model.RedeemBatch `json:"batches"`
	Total   int64               `json:"total"`
	Page    int                 `json:"page"`
	Limit   int                 `json:"limit"`
}

type AdminRedeemCodeDetail struct {
	ID                  string     `json:"id"`
	Code                string     `json:"code,omitempty"`
	CodeSuffix          string     `json:"codeSuffix"`
	Status              string     `json:"status"`
	RedeemedBy          string     `json:"redeemedBy,omitempty"`
	RedeemedUsername    string     `json:"redeemedUsername,omitempty"`
	RedeemedDisplayName string     `json:"redeemedDisplayName,omitempty"`
	RedeemedAt          *time.Time `json:"redeemedAt"`
	RedeemedIP          string     `json:"redeemedIp,omitempty"`
	ExpiresAt           *time.Time `json:"expiresAt"`
	AmountMicrocredits  int64      `json:"amountMicrocredits"`
}

type AdminRedeemCodePage struct {
	Batch              model.RedeemBatch       `json:"batch"`
	Codes              []AdminRedeemCodeDetail `json:"codes"`
	PlaintextAvailable bool                    `json:"plaintextAvailable"`
	Total              int64                   `json:"total"`
	Page               int                     `json:"page"`
	Limit              int                     `json:"limit"`
}

type BillingOrderPage struct {
	Orders []model.BillingOrder `json:"orders"`
	Total  int64                `json:"total"`
	Page   int                  `json:"page"`
	Limit  int                  `json:"limit"`
}

type CreateRedeemBatchRequest struct {
	AmountMicrocredits int64      `json:"amountMicrocredits"`
	Count              int        `json:"count"`
	Note               string     `json:"note"`
	ExpiresAt          *time.Time `json:"expiresAt"`
}

type CreateRedeemBatchResult struct {
	Batch model.RedeemBatch `json:"batch"`
	Codes []string          `json:"codes"`
}

type AdminCreditAdjustmentRequest struct {
	AmountMicrocredits int64  `json:"amountMicrocredits"`
	Note               string `json:"note"`
}

type ResolveBillingRequest struct {
	Action string `json:"action"`
	Note   string `json:"note"`
}

func (s *Service) Wallet(user *model.User, entryType string, page int, limit int) (*WalletSummary, error) {
	if user == nil {
		return nil, Unauthorized("请先登录")
	}
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	account, err := s.repo.CreditAccount(user.ID)
	if err != nil {
		return nil, err
	}
	entries, total, err := s.repo.CreditLedger(user.ID, strings.TrimSpace(entryType), limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	policy, err := s.publicCreditPolicy(user.ID)
	if err != nil {
		return nil, err
	}
	return &WalletSummary{Account: *account, Entries: entries, Total: total, Page: page, Limit: limit, Policy: policy}, nil
}

func (s *Service) RedeemCredits(user *model.User, code string, redeemedIP string) (*model.CreditAccount, error) {
	if user == nil {
		return nil, Unauthorized("请先登录")
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 32 {
		return nil, BadAuthRequest("兑换码无效或已使用")
	}
	account, err := s.repo.RedeemCode(user.ID, hashRedeemCode(code), truncateRunes(strings.TrimSpace(redeemedIP), 64))
	if errors.Is(err, repository.ErrRedeemCodeInvalid) {
		return nil, BadAuthRequest("兑换码无效或已使用")
	}
	return account, err
}

func (s *Service) AdminCreateRedeemBatch(actor *model.User, req CreateRedeemBatchRequest) (*CreateRedeemBatchResult, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if req.AmountMicrocredits <= 0 {
		return nil, BadAuthRequest("兑换码积分必须大于 0")
	}
	if req.Count <= 0 || req.Count > 5000 {
		return nil, BadAuthRequest("单批兑换码数量需为 1-5000")
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return nil, BadAuthRequest("兑换码过期时间必须晚于当前时间")
	}
	batch := model.RedeemBatch{ID: newID(), AmountMicrocredits: req.AmountMicrocredits, Count: req.Count, Note: truncateRunes(strings.TrimSpace(req.Note), 500), CreatedBy: actor.ID, ExpiresAt: req.ExpiresAt}
	codes := make([]string, 0, req.Count)
	items := make([]model.RedeemCode, 0, req.Count)
	for range req.Count {
		plain, err := newRedeemCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, plain)
		items = append(items, model.RedeemCode{
			ID: newID(), BatchID: batch.ID, CodeHash: hashRedeemCode(plain), CodeSuffix: plain[len(plain)-4:],
			AmountMicrocredits: req.AmountMicrocredits, Status: model.RedeemCodeUnused, ExpiresAt: req.ExpiresAt,
		})
	}
	encodedCodes, err := json.Marshal(codes)
	if err != nil {
		return nil, err
	}
	batch.CodesCipher, err = s.encryptSettingSecret(string(encodedCodes))
	if err != nil {
		return nil, err
	}
	// SQLite 只有一个写入器；批次生成串行进入短事务，避免并发生成占满连接池拖住全站读取。
	s.redeemBatchMu.Lock()
	defer s.redeemBatchMu.Unlock()
	if err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		if err := txRepo.CreateRedeemBatch(&batch, items); err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "redeem_batch.create", "redeem_batch", batch.ID, "创建兑换码批次", map[string]any{"count": batch.Count, "amountMicrocredits": batch.AmountMicrocredits})
	}); err != nil {
		return nil, err
	}
	return &CreateRedeemBatchResult{Batch: batch, Codes: codes}, nil
}

func (s *Service) AdminRedeemCodePage(actor *model.User, batchID string, status string, page int, limit int) (*AdminRedeemCodePage, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	batch, err := s.repo.RedeemBatch(strings.TrimSpace(batchID))
	if err != nil {
		return nil, err
	}
	page, limit = normalizeAdminPage(page, limit)
	rows, total, err := s.repo.AdminRedeemCodes(batch.ID, strings.TrimSpace(status), limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	plainCodes, err := s.redeemBatchPlainCodes(batch.CodesCipher)
	if err != nil {
		return nil, err
	}
	plainByHash := make(map[string]string, len(plainCodes))
	for _, code := range plainCodes {
		plainByHash[hashRedeemCode(code)] = code
	}
	now := time.Now()
	details := make([]AdminRedeemCodeDetail, 0, len(rows))
	for _, row := range rows {
		status := string(row.Status)
		if row.Status == model.RedeemCodeUnused && row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
			status = "expired"
		}
		details = append(details, AdminRedeemCodeDetail{
			ID: row.ID, Code: plainByHash[row.CodeHash], CodeSuffix: row.CodeSuffix, Status: status,
			RedeemedBy: row.RedeemedBy, RedeemedUsername: row.RedeemedUsername, RedeemedDisplayName: row.RedeemedDisplayName,
			RedeemedAt: row.RedeemedAt, RedeemedIP: row.RedeemedIP, ExpiresAt: row.ExpiresAt, AmountMicrocredits: row.AmountMicrocredits,
		})
	}
	batch.CodesCipher = ""
	return &AdminRedeemCodePage{Batch: *batch, Codes: details, PlaintextAvailable: len(plainCodes) > 0, Total: total, Page: page, Limit: limit}, nil
}

func (s *Service) redeemBatchPlainCodes(ciphertext string) ([]string, error) {
	if strings.TrimSpace(ciphertext) == "" {
		return nil, nil
	}
	encoded, err := s.decryptSettingSecret(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("兑换码批次密文无法解密：%w", err)
	}
	var codes []string
	if err := json.Unmarshal([]byte(encoded), &codes); err != nil {
		return nil, errors.New("兑换码批次密文内容无效")
	}
	return codes, nil
}

func (s *Service) AdminRedeemBatchPage(actor *model.User, query AdminListQuery) (*RedeemBatchPage, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	page, limit := normalizeAdminPage(query.Page, query.Limit)
	items, total, err := s.repo.AdminRedeemBatches(query.Keyword, query.Status, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	return &RedeemBatchPage{Batches: items, Total: total, Page: page, Limit: limit}, nil
}

func (s *Service) AdminAdjustCredits(actor *model.User, userID string, req AdminCreditAdjustmentRequest) (*model.CreditAccount, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if req.AmountMicrocredits == 0 {
		return nil, BadAuthRequest("调账积分不能为 0")
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return nil, BadAuthRequest("请填写调账原因")
	}
	note = truncateRunes(note, 500)
	var account *model.CreditAccount
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		if _, err := txRepo.UserForUpdate(userID); err != nil {
			return err
		}
		var err error
		account, err = txRepo.AdjustCredits(userID, actor.ID, req.AmountMicrocredits, note)
		if err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "credits.adjust", "user", userID, "管理员调整用户积分", map[string]any{"amountMicrocredits": req.AmountMicrocredits, "note": note})
	})
	if errors.Is(err, repository.ErrInsufficientCredits) {
		return nil, BadAuthRequest("用户可用积分不足，不能执行本次扣减")
	}
	if err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Service) AdminBillingOrderPage(actor *model.User, query AdminListQuery) (*BillingOrderPage, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	page, limit := normalizeAdminPage(query.Page, query.Limit)
	items, total, err := s.repo.AdminBillingOrders(query.Status, query.Keyword, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	return &BillingOrderPage{Orders: items, Total: total, Page: page, Limit: limit}, nil
}

func (s *Service) ResolveBillingOrder(actor *model.User, id string, req ResolveBillingRequest) (*model.BillingOrder, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return nil, BadAuthRequest("请填写核对依据")
	}
	note = truncateRunes(note, 500)
	action := strings.TrimSpace(req.Action)
	if action != "settle" && action != "refund" {
		return nil, BadAuthRequest("请选择结算或退款")
	}
	id = strings.TrimSpace(id)
	var resolved *model.BillingOrder
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		order, err := txRepo.BillingOrderForUpdate(id)
		if err != nil {
			return err
		}
		if order.Status != model.BillingStatusUncertain && order.Status != model.BillingStatusRunning && order.Status != model.BillingStatusReserved {
			return BadAuthRequest("当前订单不需要人工核对")
		}
		if action == "settle" {
			err = txRepo.SettleBillingOrder(id, order.ProviderRequestID)
		} else {
			err = txRepo.RefundBillingOrder(id, note)
		}
		if err != nil {
			return err
		}
		if err := txRepo.RecordBillingResolution(id, actor.ID, note); err != nil {
			return err
		}
		if err := appendAdminAuditWithRepository(txRepo, actor, "billing.resolve", "user", order.UserID, "人工核对用户计费订单", map[string]any{"billingOrderId": id, "action": action, "note": note}); err != nil {
			return err
		}
		resolved, err = txRepo.BillingOrder(id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func (s *Service) AdminDisableRedeemBatch(actor *model.User, batchID string) (int64, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return 0, err
	}
	batchID = strings.TrimSpace(batchID)
	var count int64
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		if _, err := txRepo.RedeemBatch(batchID); err != nil {
			return err
		}
		var err error
		count, err = txRepo.DisableRedeemBatch(batchID, time.Now())
		if err != nil {
			return err
		}
		if count == 0 {
			return BadAuthRequest("该批次没有可禁用的兑换码")
		}
		return appendAdminAuditWithRepository(txRepo, actor, "redeem_batch.disable", "redeem_batch", batchID, "禁用批次内全部未使用兑换码", map[string]any{"disabledCount": count})
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) AdminDisableRedeemCode(actor *model.User, batchID string, codeID string) error {
	if err := s.RequireAdmin(actor); err != nil {
		return err
	}
	batchID = strings.TrimSpace(batchID)
	codeID = strings.TrimSpace(codeID)
	return s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		disabled, err := txRepo.DisableRedeemCode(batchID, codeID, time.Now())
		if err != nil {
			return err
		}
		if !disabled {
			return BadAuthRequest("兑换码不存在、已使用、已禁用或已过期")
		}
		return appendAdminAuditWithRepository(txRepo, actor, "redeem_code.disable", "redeem_code", codeID, "禁用单个兑换码", map[string]any{"batchId": batchID})
	})
}

func (s *Service) taskBillingOrder(userID string, task *model.Task, input map[string]any) (*model.BillingOrder, error) {
	config, _ := input["config"].(map[string]any)
	if config == nil {
		return nil, nil
	}
	channelID, _ := config["channelId"].(string)
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		baseURL, _ := config["baseUrl"].(string)
		channelID = systemChannelIDFromBaseURL(baseURL)
	}
	if channelID == "" {
		return nil, nil
	}
	modelKey, _ := config["model"].(string)
	modelKey = strings.TrimPrefix(strings.TrimSpace(modelKey), "models/")
	capability := normalizeCapability(fmt.Sprint(input["mode"]))
	if capability == "" {
		capability = capabilityFromTaskType(task.Type)
	}
	scene := firstNonEmpty(strings.TrimSpace(task.Operation), task.Type)
	return s.newBillingOrder(userID, task.ID, "task:"+task.ID+":"+newID(), channelID, modelKey, capability, scene, billingQuantity(capability, config["videoSeconds"]))
}

func (s *Service) PrepareProxyBillingIdempotency(userID string, idempotencyKey string) (string, error) {
	key, err := normalizeProxyBillingIdempotencyKey(idempotencyKey)
	if err != nil {
		return "", err
	}
	existing, err := s.repo.BillingOrderByIdempotencyKey(userID, proxyBillingIdempotencyPrefix+key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return key, nil
	}
	if err != nil {
		return "", err
	}
	return "", proxyBillingIdempotencyConflict(existing)
}

func (s *Service) ReserveProxyBilling(userID string, channelID string, modelKey string, capability string, scene string, idempotencyKey string, quantity int64) (*model.BillingOrder, error) {
	key, err := normalizeProxyBillingIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	fullKey := proxyBillingIdempotencyPrefix + key
	if existing, lookupErr := s.repo.BillingOrderByIdempotencyKey(userID, fullKey); lookupErr == nil {
		return nil, proxyBillingIdempotencyConflict(existing)
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return nil, lookupErr
	}
	order, err := s.newBillingOrder(userID, "", fullKey, channelID, modelKey, capability, firstNonEmpty(strings.TrimSpace(scene), "system_proxy"), quantity)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ReserveBillingOrder(order); err != nil {
		// 预查与写入之间仍可能有并发请求，唯一索引负责最终仲裁；命中后统一返回安全冲突，不暴露数据库错误。
		if existing, lookupErr := s.repo.BillingOrderByIdempotencyKey(userID, fullKey); lookupErr == nil {
			return nil, proxyBillingIdempotencyConflict(existing)
		}
		if errors.Is(err, repository.ErrInsufficientCredits) {
			return nil, BadAuthRequest("积分不足，请先使用兑换码充值")
		}
		return nil, err
	}
	return order, nil
}

func normalizeProxyBillingIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" {
		return "", BadAuthRequest("系统渠道付费请求缺少 X-Idempotency-Key，本次未调用供应商")
	}
	if len(key) > proxyBillingIdempotencyMaxLength {
		return "", BadAuthRequest("X-Idempotency-Key 不能超过 120 个字符")
	}
	for _, char := range key {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return "", BadAuthRequest("X-Idempotency-Key 只能包含字母、数字、短横线、下划线、点和冒号")
	}
	return key, nil
}

func proxyBillingIdempotencyConflict(order *model.BillingOrder) error {
	if order == nil {
		return Conflict("同一请求标识已经使用；本次未再次调用供应商，请核对原请求后再决定是否创建新的明确操作")
	}
	switch order.Status {
	case model.BillingStatusReserved, model.BillingStatusRunning:
		return Conflict("同一请求标识的系统渠道调用已受理或仍在执行；本次未再次调用供应商，请查看原请求明细")
	case model.BillingStatusUncertain:
		return Conflict("同一请求标识的原调用费用状态待核对；本次未再次调用供应商，请先核对请求明细和供应商账单")
	case model.BillingStatusSettled:
		return Conflict("同一请求标识的原调用已经完成结算；系统不会重复调用供应商或重放响应，确认需要新调用后请创建新的操作")
	case model.BillingStatusRefunded:
		return Conflict("同一请求标识的原调用已经退款；该标识不能复用，确认需要新调用后请创建新的操作")
	default:
		return Conflict("同一请求标识已经使用；本次未再次调用供应商，请查看原请求明细")
	}
}

func (s *Service) newBillingOrder(userID string, taskID string, idempotencyKey string, channelID string, modelKey string, capability string, scene string, requestedQuantity int64) (*model.BillingOrder, error) {
	item, err := s.repo.ChannelModelByKey(channelID, modelKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, BadAuthRequest("当前系统渠道模型未配置或已停用")
	}
	if err != nil {
		return nil, err
	}
	if !item.PriceConfigured {
		return nil, BadAuthRequest("当前模型尚未配置用户积分价格")
	}
	if item.Capability != capability {
		return nil, BadAuthRequest("当前系统模型能力与请求类型不匹配")
	}
	quantity := int64(1)
	switch item.BillingMode {
	case "fixed_request":
	case "per_second":
		if item.Capability != "video" || capability != "video" {
			return nil, BadAuthRequest("按秒计费仅适用于视频生成")
		}
		if requestedQuantity <= 0 {
			return nil, BadAuthRequest("视频生成时长无效，无法按秒计费")
		}
		quantity = requestedQuantity
	default:
		return nil, BadAuthRequest("当前模型计费方式暂不支持")
	}
	policy, err := s.creditPolicy()
	if err != nil {
		return nil, err
	}
	multiplierBPS := policy.DefaultMultiplierBPS
	if configured := policy.ModelMultiplierBPS[modelKey]; configured > 0 {
		multiplierBPS = configured
	}
	amount, err := creditAmount(item.UnitPriceMicrocredits, quantity, multiplierBPS)
	if err != nil {
		return nil, err
	}
	return &model.BillingOrder{
		ID: newID(), UserID: userID, IdempotencyKey: idempotencyKey, TaskID: taskID,
		ChannelID: channelID, ChannelModelID: item.ID, Model: modelKey, Capability: capability,
		Scene: truncateRunes(scene, 80), BillingMode: item.BillingMode, PriceVersion: item.PriceVersion,
		UnitPriceMicrocredits: item.UnitPriceMicrocredits, MultiplierBasisPoints: multiplierBPS, Quantity: quantity, AmountMicrocredits: amount,
		Status: model.BillingStatusReserved,
	}, nil
}

func billingQuantity(capability string, value any) int64 {
	if capability != "video" {
		return 1
	}
	quantity, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
	if err != nil || quantity <= 0 {
		return 0
	}
	return quantity
}

func (s *Service) MarkBillingRunning(orderID string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.MarkBillingRunning(orderID)
}

func (s *Service) SettleBilling(orderID string, providerRequestID string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.SettleBillingOrder(orderID, providerRequestID)
}

func (s *Service) SettleBillingFromExecution(orderID string, providerRequestID string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.SettleBillingOrderFromExecution(orderID, providerRequestID)
}

func (s *Service) RefundBilling(orderID string, errorText string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.RefundBillingOrder(orderID, truncateRunes(errorText, 1000))
}

func (s *Service) RefundBillingFromExecution(orderID string, errorText string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.RefundBillingOrderFromExecution(orderID, truncateRunes(errorText, 1000))
}

func (s *Service) MarkBillingUncertain(orderID string, errorText string) error {
	if orderID == "" {
		return nil
	}
	return s.repo.MarkBillingUncertain(orderID, truncateRunes(errorText, 1000))
}

func (s *Service) BillingFailureRequiresReview(orderID string, taskID string, err error) bool {
	if billingFailureUncertain(err) {
		return true
	}
	hasSuccessfulCall, logErr := s.repo.TaskHasSuccessfulBillableCall(taskID)
	if logErr != nil || hasSuccessfulCall {
		return true
	}
	if orderID == "" {
		return false
	}
	order, orderErr := s.repo.BillingOrder(orderID)
	if orderErr != nil || order.Status == model.BillingStatusUncertain {
		return true
	}
	return false
}

type providerBillingReviewError struct {
	reason string
	cause  error
}

func (e providerBillingReviewError) Error() string {
	return e.reason
}

func (e providerBillingReviewError) Unwrap() error { return e.cause }

func billingFailureUncertain(err error) bool {
	if err == nil {
		return false
	}
	// 已在建连前失败是可证明的无供应商调用边界；错误原因即使包含 timeout 也不能误转为费用待核对。
	if providerRequestDefinitelyNotSent(err) {
		return false
	}
	var slotErr channelSlotError
	if errors.As(err, &slotErr) {
		return false
	}
	var reviewErr providerBillingReviewError
	if errors.As(err, &reviewErr) {
		return true
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) && ProviderHTTPStatusRequiresBillingReview(httpErr.StatusCode) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"524", "timeout", "超时", "deadline exceeded", "context canceled", "connection reset", "unexpected eof", "broken pipe", "上游流式", "stream_incomplete", "上游响应超过"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// ProviderHTTPStatusRequiresBillingReview 只处理已经收到的上游 HTTP 响应；调用前的本地 5xx/504 必须由入口自行区分。
func ProviderHTTPStatusRequiresBillingReview(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, 499:
		return true
	case http.StatusNotImplemented:
		// 501 明确表示接口未实现，文本兼容层可安全切换到另一条协议路径。
		return false
	default:
		return statusCode >= http.StatusInternalServerError && statusCode <= 599
	}
}

func ProviderHTTPBillingReviewMessage(statusCode int) string {
	switch statusCode {
	case http.StatusRequestTimeout:
		return "上游返回请求超时（408）：原请求可能仍在服务端执行并产生费用；不要无确认地立即重试，确认可能重复计费后可在原任务点击“重试”继续"
	case 499:
		return "上游返回连接已关闭（499）：原请求状态不确定且可能已经计费；确认可能重复计费后可在原任务点击“重试”继续"
	case 524:
		return "上游网关超时（524）：若请求约在 5 分钟被截断且供应商记录为非流式，通常是供应商或 CDN 的固定等待上限，调大本系统超时无效；模型可能仍在服务端执行并产生费用。可先确认风险，再在原任务点击“重试”或改用能持续送出分片的 SSE 接口"
	default:
		return fmt.Sprintf("上游返回 HTTP %d：网关或模型服务异常，原请求可能仍在执行并产生费用；确认可能重复计费后可在原任务点击“重试”继续", statusCode)
	}
}

func newRedeemCode() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return hex.EncodeToString(raw[:]), nil
}

func hashRedeemCode(code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}

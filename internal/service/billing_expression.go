package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
	"routerx/internal/relay"
)

type relayBillingResult struct {
	QuotaUsed          int64
	PriceSource        string
	ExpressionSource   string
	ExpressionSnapshot map[string]interface{}
	MultiplierSnapshot map[string]interface{}
}

type relayBillingPriceRule struct {
	ID              uint
	Source          string
	ChannelID       uint
	Model           string
	PriceMode       string
	PriceExpression string
	VariablesJSON   model.JSONValue
	UnitTokens      int64
	RuleVersion     int64
}

type relayBillingMultiplier struct {
	EffectiveRatio float64
	Snapshot       map[string]interface{}
}

func (s *RelayService) calculateRelayBilling(token *model.Token, channel *model.Channel, modelName string, usage *relay.Usage) relayBillingResult {
	baseQuota := quotaFromUsage(usage)
	multiplier := s.calculateRelayBillingMultiplier(token, channel)
	quotaUsed := applyRelayBillingMultiplier(baseQuota, multiplier.EffectiveRatio)
	usageSource := logUsageSource(usage, common.LogStatusSuccess, quotaUsed)
	fallbackSource := "p0_usage"
	if usageSource == common.LogUsageSourceMinimum {
		fallbackSource = "minimum"
	}
	fallback := relayBillingResult{
		QuotaUsed:          quotaUsed,
		PriceSource:        fallbackSource,
		ExpressionSource:   fallbackSource,
		ExpressionSnapshot: buildP0BillingExpressionSnapshot(usage, usageSource, baseQuota),
		MultiplierSnapshot: multiplier.Snapshot,
	}

	// 价格规则由管理员维护。运行时如果规则缺失、表达式无效或数据库读取失败，
	// 保守回退到 P0 usage/minimum 计费，避免一次错误配置阻断所有模型调用。
	rule, err := resolveRelayBillingPriceRule(channel, modelName)
	if err != nil || rule == nil {
		return fallback
	}
	baseQuota, variables, err := evaluateRelayBillingExpression(rule, usage)
	if err != nil {
		return fallback
	}
	quotaUsed = applyRelayBillingMultiplier(baseQuota, multiplier.EffectiveRatio)
	return relayBillingResult{
		QuotaUsed:        quotaUsed,
		PriceSource:      rule.Source,
		ExpressionSource: rule.Source,
		ExpressionSnapshot: map[string]interface{}{
			"source":       rule.Source,
			"id":           rule.ID,
			"channel_id":   rule.ChannelID,
			"model":        rule.Model,
			"price_mode":   rule.PriceMode,
			"expression":   rule.PriceExpression,
			"base_quota":   baseQuota,
			"unit_tokens":  normalizedBillingUnitTokens(rule.UnitTokens),
			"rule_version": rule.RuleVersion,
			"variables":    variables,
		},
		MultiplierSnapshot: multiplier.Snapshot,
	}
}

func resolveRelayBillingPriceRule(channel *model.Channel, modelName string) (*relayBillingPriceRule, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, nil
	}
	if channel != nil && channel.ID > 0 {
		var channelPrice model.ChannelModelPrice
		err := internal.DB.
			Where("channel_id = ? AND model = ? AND enabled = ?", channel.ID, modelName, true).
			First(&channelPrice).Error
		if err == nil {
			return relayBillingPriceRuleFromChannelPrice(channelPrice), nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	var price model.ModelPrice
	err := internal.DB.Where("model = ? AND enabled = ?", modelName, true).First(&price).Error
	if err == nil {
		return relayBillingPriceRuleFromModelPrice(price), nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return nil, err
}

func relayBillingPriceRuleFromModelPrice(price model.ModelPrice) *relayBillingPriceRule {
	return &relayBillingPriceRule{
		ID:              price.ID,
		Source:          "model_prices",
		Model:           price.Model,
		PriceMode:       price.PriceMode,
		PriceExpression: price.PriceExpression,
		VariablesJSON:   price.VariablesJSON,
		UnitTokens:      price.UnitTokens,
		RuleVersion:     price.RuleVersion,
	}
}

func relayBillingPriceRuleFromChannelPrice(price model.ChannelModelPrice) *relayBillingPriceRule {
	return &relayBillingPriceRule{
		ID:              price.ID,
		Source:          "channel_model_prices",
		ChannelID:       price.ChannelID,
		Model:           price.Model,
		PriceMode:       price.PriceMode,
		PriceExpression: price.PriceExpression,
		VariablesJSON:   price.VariablesJSON,
		UnitTokens:      price.UnitTokens,
		RuleVersion:     price.RuleVersion,
	}
}

func evaluateRelayBillingExpression(rule *relayBillingPriceRule, usage *relay.Usage) (int64, map[string]interface{}, error) {
	if rule == nil || strings.TrimSpace(rule.PriceExpression) == "" {
		return 0, nil, errors.New("billing price expression is required")
	}
	variables, err := relayBillingExpressionVariables(rule, usage)
	if err != nil {
		return 0, nil, err
	}
	program, err := expr.Compile(
		rule.PriceExpression,
		expr.Env(variables),
		expr.AsAny(),
		expr.DisableAllBuiltins(),
		expr.MaxNodes(128),
	)
	if err != nil {
		return 0, nil, err
	}
	value, err := expr.Run(program, variables)
	if err != nil {
		return 0, nil, err
	}
	quotaUsed, err := quotaFromBillingExpressionValue(value)
	if err != nil {
		return 0, nil, err
	}
	return quotaUsed, variables, nil
}

func relayBillingExpressionVariables(rule *relayBillingPriceRule, usage *relay.Usage) (map[string]interface{}, error) {
	variables := map[string]interface{}{}
	if len(rule.VariablesJSON) > 0 {
		if err := json.Unmarshal(rule.VariablesJSON, &variables); err != nil {
			return nil, err
		}
	}
	// usage 和 unit_tokens 是结算事实，必须由运行时覆盖配置变量，防止历史账单被人为变量污染。
	if usage != nil {
		variables["prompt_tokens"] = usage.PromptTokens
		variables["completion_tokens"] = usage.CompletionTokens
		variables["total_tokens"] = usage.TotalTokens
	} else {
		variables["prompt_tokens"] = 0
		variables["completion_tokens"] = 0
		variables["total_tokens"] = 0
	}
	variables["request_count"] = 1
	variables["minimum_quota"] = 1
	variables["unit_tokens"] = normalizedBillingUnitTokens(rule.UnitTokens)
	return variables, nil
}

func normalizedBillingUnitTokens(unitTokens int64) int64 {
	if unitTokens <= 0 {
		return 1
	}
	return unitTokens
}

func quotaFromBillingExpressionValue(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int:
		return nonNegativeQuota(float64(v))
	case int8:
		return nonNegativeQuota(float64(v))
	case int16:
		return nonNegativeQuota(float64(v))
	case int32:
		return nonNegativeQuota(float64(v))
	case int64:
		return nonNegativeQuota(float64(v))
	case uint:
		return nonNegativeQuota(float64(v))
	case uint8:
		return nonNegativeQuota(float64(v))
	case uint16:
		return nonNegativeQuota(float64(v))
	case uint32:
		return nonNegativeQuota(float64(v))
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("billing quota %d exceeds int64", v)
		}
		return nonNegativeQuota(float64(v))
	case float32:
		return nonNegativeQuota(float64(v))
	case float64:
		return nonNegativeQuota(v)
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0, err
		}
		return nonNegativeQuota(n)
	default:
		return 0, fmt.Errorf("billing expression returned %T, want number", value)
	}
}

func nonNegativeQuota(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("billing quota must be finite")
	}
	if value < 0 {
		return 0, errors.New("billing quota must be non-negative")
	}
	if value > float64(math.MaxInt64) {
		return 0, errors.New("billing quota exceeds int64")
	}
	return int64(math.Ceil(value)), nil
}

func (s *RelayService) calculateRelayBillingMultiplier(token *model.Token, channel *model.Channel) relayBillingMultiplier {
	userGroupKeys := userGroupAccessKeys(token)
	userGroup := "default"
	if len(userGroupKeys) > 0 {
		userGroup = userGroupKeys[0]
	}
	channelGroup := "default"
	if channel != nil {
		channelGroup = normalizeChannelGroupName(channel.ChannelGroup)
	}

	defaultRatio := s.billingDefaultRatio()
	userGroupRatio := billingRatioForKeys(s.billingRatioMap("billing.user_group_ratios"), userGroupKeys, 1)
	channelGroupRatio := billingRatioForKeys(s.billingRatioMap("billing.channel_group_ratios"), []string{channelGroup}, 1)
	userGroupChannelRatio, hasUserGroupChannelRatio := billingNestedRatioForKeys(s.billingNestedRatioMap("billing.user_group_channel_ratios"), userGroupKeys, channelGroup, 1)
	ratioMode := "separate_factors"
	effectiveRatio := defaultRatio * userGroupRatio * channelGroupRatio
	if hasUserGroupChannelRatio {
		// 组合倍率表达“这个用户分组使用这个通道分组”的最终业务倍率，
		// 避免把用户分组倍率和通道分组倍率重复叠加一次。
		ratioMode = "user_group_channel_override"
		effectiveRatio = defaultRatio * userGroupChannelRatio
	}
	if !validPositiveRatio(effectiveRatio) {
		effectiveRatio = 1
	}

	return relayBillingMultiplier{
		EffectiveRatio: effectiveRatio,
		Snapshot: map[string]interface{}{
			"source":                   "settings",
			"default_ratio":            defaultRatio,
			"user_group":               userGroup,
			"user_group_ratio":         userGroupRatio,
			"channel_group":            channelGroup,
			"channel_group_ratio":      channelGroupRatio,
			"user_group_channel_ratio": userGroupChannelRatio,
			"ratio_mode":               ratioMode,
			"effective_ratio":          effectiveRatio,
		},
	}
}

func (s *RelayService) billingDefaultRatio() float64 {
	if s == nil || s.settingService == nil {
		return 1
	}
	raw, err := s.settingService.Get("billing.default_ratio")
	if err != nil {
		return 1
	}
	ratio, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !validPositiveRatio(ratio) {
		return 1
	}
	return ratio
}

func (s *RelayService) billingRatioMap(key string) map[string]float64 {
	if s == nil || s.settingService == nil {
		return nil
	}
	raw, err := s.settingService.Get(key)
	if err != nil {
		return nil
	}
	values := map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	normalized := make(map[string]float64, len(values))
	for key, ratio := range values {
		key = strings.TrimSpace(key)
		if key == "" || !validPositiveRatio(ratio) {
			continue
		}
		normalized[key] = ratio
	}
	return normalized
}

func (s *RelayService) billingNestedRatioMap(key string) map[string]map[string]float64 {
	if s == nil || s.settingService == nil {
		return nil
	}
	raw, err := s.settingService.Get(key)
	if err != nil {
		return nil
	}
	values := map[string]map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	normalized := make(map[string]map[string]float64, len(values))
	for userGroup, channelRatios := range values {
		userGroup = strings.TrimSpace(userGroup)
		if userGroup == "" {
			continue
		}
		cleanChannelRatios := make(map[string]float64, len(channelRatios))
		for channelGroup, ratio := range channelRatios {
			channelGroup = normalizeChannelGroupName(channelGroup)
			if !validPositiveRatio(ratio) {
				continue
			}
			cleanChannelRatios[channelGroup] = ratio
		}
		if len(cleanChannelRatios) > 0 {
			normalized[userGroup] = cleanChannelRatios
		}
	}
	return normalized
}

func billingRatioForKeys(values map[string]float64, keys []string, fallback float64) float64 {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if ratio, ok := values[key]; ok && validPositiveRatio(ratio) {
			return ratio
		}
	}
	if ratio, ok := values["default"]; ok && validPositiveRatio(ratio) {
		return ratio
	}
	return fallback
}

func billingNestedRatioForKeys(values map[string]map[string]float64, userGroupKeys []string, channelGroup string, fallback float64) (float64, bool) {
	channelGroup = normalizeChannelGroupName(channelGroup)
	for _, userGroup := range userGroupKeys {
		userGroup = strings.TrimSpace(userGroup)
		if userGroup == "" {
			continue
		}
		if channelRatios, ok := values[userGroup]; ok {
			if ratio, ok := channelRatios[channelGroup]; ok && validPositiveRatio(ratio) {
				return ratio, true
			}
			if ratio, ok := channelRatios["default"]; ok && validPositiveRatio(ratio) {
				return ratio, true
			}
		}
	}
	if channelRatios, ok := values["default"]; ok {
		if ratio, ok := channelRatios[channelGroup]; ok && validPositiveRatio(ratio) {
			return ratio, true
		}
		if ratio, ok := channelRatios["default"]; ok && validPositiveRatio(ratio) {
			return ratio, true
		}
	}
	return fallback, false
}

func applyRelayBillingMultiplier(baseQuota int64, effectiveRatio float64) int64 {
	if baseQuota <= 0 {
		return 0
	}
	if !validPositiveRatio(effectiveRatio) {
		effectiveRatio = 1
	}
	quota := int64(math.Ceil(float64(baseQuota) * effectiveRatio))
	if quota < 1 {
		return 1
	}
	return quota
}

func validPositiveRatio(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func billingMultiplierSnapshot(billing relayBillingResult) map[string]interface{} {
	if billing.MultiplierSnapshot == nil {
		return defaultMultiplierSnapshot()
	}
	return billing.MultiplierSnapshot
}

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

func (s *RelayService) calculateRelayBilling(channel *model.Channel, modelName string, usage *relay.Usage) relayBillingResult {
	fallbackQuota := quotaFromUsage(usage)
	usageSource := logUsageSource(usage, common.LogStatusSuccess, fallbackQuota)
	fallbackSource := "p0_usage"
	if usageSource == common.LogUsageSourceMinimum {
		fallbackSource = "minimum"
	}
	fallback := relayBillingResult{
		QuotaUsed:          fallbackQuota,
		PriceSource:        fallbackSource,
		ExpressionSource:   fallbackSource,
		ExpressionSnapshot: buildP0BillingExpressionSnapshot(usage, usageSource, fallbackQuota),
	}

	// 价格规则由管理员维护。运行时如果规则缺失、表达式无效或数据库读取失败，
	// 保守回退到 P0 usage/minimum 计费，避免一次错误配置阻断所有模型调用。
	rule, err := resolveRelayBillingPriceRule(channel, modelName)
	if err != nil || rule == nil {
		return fallback
	}
	quotaUsed, variables, err := evaluateRelayBillingExpression(rule, usage)
	if err != nil {
		return fallback
	}
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
			"base_quota":   quotaUsed,
			"unit_tokens":  normalizedBillingUnitTokens(rule.UnitTokens),
			"rule_version": rule.RuleVersion,
			"variables":    variables,
		},
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

package dto

import (
	"encoding/json"
	"strings"

	"routerx/internal/model"
)

func UserBriefFromModel(user *model.User) UserBrief {
	if user == nil {
		return UserBrief{}
	}
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	email := ""
	if user.Email != nil {
		email = *user.Email
	}
	return UserBrief{
		ID:          user.ID,
		Username:    username,
		DisplayName: user.DisplayName,
		Email:       email,
		Role:        user.Role,
		Quota:       user.Quota,
		Status:      user.Status,
		GroupID:     user.GroupID,
	}
}

func UserGroupInfoFromModel(group *model.Group) UserGroupInfo {
	if group == nil {
		return UserGroupInfo{}
	}
	return UserGroupInfo{
		ID:        group.ID,
		Name:      group.Name,
		Ratio:     group.Ratio,
		CreatedAt: group.CreatedAt,
	}
}

func UserGroupInfosFromModels(groups []model.Group) []UserGroupInfo {
	items := make([]UserGroupInfo, 0, len(groups))
	for i := range groups {
		items = append(items, UserGroupInfoFromModel(&groups[i]))
	}
	return items
}

func ChannelInfoFromModel(channel *model.Channel) ChannelInfo {
	if channel == nil {
		return ChannelInfo{}
	}
	baseURLs := decodeStringSlice(channel.BaseURLs)
	apiKeys := decodeStringSlice(channel.APIKeys)
	upstreams := decodeUpstreamPublic(channel.Upstreams)
	apiKeyCount := len(apiKeys)
	if strings.TrimSpace(channel.APIKey) != "" {
		apiKeyCount++
	}
	for _, upstream := range upstreams {
		if upstream.HasAPIKey {
			apiKeyCount++
		}
	}
	return ChannelInfo{
		ID:               channel.ID,
		Idx:              channel.Idx,
		Type:             channel.Type,
		Name:             channel.Name,
		Models:           channel.Models,
		BaseURL:          channel.BaseURL,
		BaseURLs:         baseURLs,
		KeySelectionMode: channel.KeySelectionMode,
		APIKeyCount:      apiKeyCount,
		Upstreams:        upstreams,
		ModelRewrites:    rawJSON(channel.ModelRewrites),
		Group:            channel.ChannelGroup,
		UpstreamOptions:  rawJSON(channel.UpstreamOptions),
		Priority:         channel.Priority,
		Weight:           channel.Weight,
		Status:           channel.Status,
		ResponseMs:       channel.ResponseMs,
		Balance:          channel.Balance,
		ErrorCount:       channel.ErrorCount,
		CreatedAt:        channel.CreatedAt,
		UpdatedAt:        channel.UpdatedAt,
	}
}

func ChannelInfosFromModels(channels []model.Channel) []ChannelInfo {
	result := make([]ChannelInfo, 0, len(channels))
	for i := range channels {
		result = append(result, ChannelInfoFromModel(&channels[i]))
	}
	return result
}

func decodeStringSlice(raw model.JSONValue) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func decodeUpstreamPublic(raw model.JSONValue) []ChannelUpstreamPublic {
	if len(raw) == 0 {
		return nil
	}
	var upstreams []ChannelUpstreamRequest
	if err := json.Unmarshal(raw, &upstreams); err != nil {
		return nil
	}
	result := make([]ChannelUpstreamPublic, 0, len(upstreams))
	for _, upstream := range upstreams {
		baseURL := strings.TrimSpace(upstream.BaseURL)
		if baseURL == "" && strings.TrimSpace(upstream.APIKey) == "" {
			continue
		}
		result = append(result, ChannelUpstreamPublic{
			BaseURL:   baseURL,
			HasAPIKey: strings.TrimSpace(upstream.APIKey) != "",
		})
	}
	return result
}

func rawJSON(raw model.JSONValue) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(raw)
}

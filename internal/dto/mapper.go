package dto

import "routerx/internal/model"

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

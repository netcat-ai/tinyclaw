package wecom

import "context"

// InternalUser 企业微信内部成员详情
type InternalUser struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
	Alias  string `json:"alias,omitempty"`
	Email  string `json:"email,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type internalUserResponse struct {
	APIRes
	InternalUser
}

// GetUser 获取企业微信内部成员详情
func (c *Client) GetUser(ctx context.Context, userID string) (*InternalUser, error) {
	res := &internalUserResponse{}
	if err := c.Get(ctx, "/cgi-bin/user/get", res, "userid="+userID); err != nil {
		return nil, err
	}
	if err := res.Error(); err != nil {
		return nil, err
	}
	return &res.InternalUser, nil
}

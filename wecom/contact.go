package wecom

import "context"

// ExternalContact 外部联系人详情
type ExternalContact struct {
	ExternalUserID string `json:"external_userid"`
	Name           string `json:"name"`
	Type           int    `json:"type"` // 1=微信用户 2=企业微信用户
	CorpName       string `json:"corp_name"`
}

// GroupChat 客户群详情
type GroupChat struct {
	ChatID string `json:"chat_id"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
}

// GetExternalContact 获取外部联系人详情
func (c *Client) GetExternalContact(ctx context.Context, externalUserID string) (*ExternalContact, error) {
	res := &struct {
		APIRes
		ExternalContact ExternalContact `json:"external_contact"`
	}{}
	if err := c.Get(ctx, "/cgi-bin/externalcontact/get", res, "external_userid="+externalUserID); err != nil {
		return nil, err
	}
	if err := res.Error(); err != nil {
		return nil, err
	}
	return &res.ExternalContact, nil
}

// GetGroupChat 获取客户群详情
func (c *Client) GetGroupChat(ctx context.Context, chatID string) (*GroupChat, error) {
	req := map[string]any{
		"chat_id":   chatID,
		"need_name": 1,
	}
	res := &struct {
		APIRes
		GroupChat GroupChat `json:"group_chat"`
	}{}
	if err := c.Post(ctx, "/cgi-bin/externalcontact/groupchat/get", req, res); err != nil {
		return nil, err
	}
	if err := res.Error(); err != nil {
		return nil, err
	}
	return &res.GroupChat, nil
}

// GetArchiveGroupChat 通过会话存档接口获取内部群信息
func (c *Client) GetArchiveGroupChat(ctx context.Context, roomID string) (*GroupChat, error) {
	req := map[string]any{
		"roomid": roomID,
	}
	res := &struct {
		APIRes
		RoomName string `json:"roomname"`
	}{}
	if err := c.Post(ctx, "/cgi-bin/msgaudit/groupchat/get", req, res); err != nil {
		return nil, err
	}
	if err := res.Error(); err != nil {
		return nil, err
	}
	return &GroupChat{
		ChatID: roomID,
		Name:   res.RoomName,
	}, nil
}

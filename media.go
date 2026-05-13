package main

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"tinyclaw/wecom/finance"
)

var (
	errMediaMessageNotFound  = errors.New("media message not found")
	errMediaPayloadMismatch  = errors.New("media payload mismatch")
	errMediaNotDownloadable  = errors.New("message is not a downloadable image")
	errMediaDownloadDisabled = errors.New("media download is disabled")
)

type mediaLookupStore interface {
	GetMessageByIdentity(context.Context, string, string, int64, string) (MessageRecord, bool, error)
}

type mediaSDK interface {
	GetMediaData(indexBuf string, sdkFileID string) (*finance.MediaData, error)
}

type mediaBlob struct {
	Data        []byte
	ContentType string
	FileName    string
}

type mediaFetchRequest struct {
	RoomID    string `json:"room_id"`
	Seq       int64  `json:"seq"`
	MsgID     string `json:"msgid"`
	SDKFileID string `json:"sdk_file_id"`
}

type clawmanMediaService struct {
	tenantID string
	store    mediaLookupStore
	sdk      mediaSDK
}

func (s *clawmanMediaService) FetchImage(ctx context.Context, req mediaFetchRequest) (mediaBlob, error) {
	if s == nil || s.store == nil || s.sdk == nil {
		return mediaBlob{}, errMediaDownloadDisabled
	}
	if strings.TrimSpace(req.RoomID) == "" || req.Seq <= 0 || strings.TrimSpace(req.MsgID) == "" {
		return mediaBlob{}, fmt.Errorf("room_id, seq, and msgid are required")
	}
	if strings.TrimSpace(req.SDKFileID) == "" {
		return mediaBlob{}, fmt.Errorf("sdk_file_id is required")
	}

	record, ok, err := s.store.GetMessageByIdentity(ctx, s.tenantID, req.RoomID, req.Seq, req.MsgID)
	if err != nil {
		return mediaBlob{}, err
	}
	if !ok {
		return mediaBlob{}, errMediaMessageNotFound
	}

	payload, err := parseWeComPayload(record.Payload)
	if err != nil {
		return mediaBlob{}, err
	}
	if payload.MsgType != "image" || payload.Image == nil || strings.TrimSpace(payload.Image.SDKFileID) == "" {
		return mediaBlob{}, errMediaNotDownloadable
	}
	if payload.Image.SDKFileID != strings.TrimSpace(req.SDKFileID) {
		return mediaBlob{}, errMediaPayloadMismatch
	}

	data, err := fetchMediaBytes(ctx, s.sdk, payload.Image.SDKFileID)
	if err != nil {
		return mediaBlob{}, err
	}
	contentType := http.DetectContentType(data)
	fileName := buildMediaFileName(record.MsgID, contentType, payload.Image.URL)
	return mediaBlob{
		Data:        data,
		ContentType: contentType,
		FileName:    fileName,
	}, nil
}

func fetchMediaBytes(ctx context.Context, sdk mediaSDK, sdkFileID string) ([]byte, error) {
	var data []byte
	indexBuf := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := sdk.GetMediaData(indexBuf, sdkFileID)
		if err != nil {
			return nil, fmt.Errorf("fetch media chunk: %w", err)
		}
		data = append(data, chunk.Data...)
		if chunk.IsFinish {
			return data, nil
		}
		indexBuf = chunk.OutIndexBuf
	}
}

func buildMediaFileName(msgID, contentType, rawURL string) string {
	ext := extensionFromURL(rawURL)
	if ext == "" {
		ext = extensionFromContentType(contentType)
	}
	if ext == "" {
		ext = ".bin"
	}
	return sanitizeFileStem(msgID) + ext
}

func extensionFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	ext := path.Ext(parsed.Path)
	if ext == "" {
		return ""
	}
	return strings.ToLower(ext)
}

func extensionFromContentType(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	exts, err := mime.ExtensionsByType(contentType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}

func sanitizeFileStem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "media"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "media"
	}
	return b.String()
}

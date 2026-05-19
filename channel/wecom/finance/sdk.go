// Package finance provides a Go wrapper around the WeCom Finance SDK
package finance

import (
	"encoding/json"
	"strconv"
	"unsafe"
)

// SDK wraps the native SDK handle plus configuration used by cgo calls.
type SDK struct {
	ptr        unsafe.Pointer
	privateKey string
	proxy      unsafe.Pointer
	passwd     unsafe.Pointer
	timeout    int
}

type SDKErr struct {
	Code int
}

func (e *SDKErr) Error() string {
	return "SDK error with code: " + strconv.Itoa(e.Code)
}

func NewSDKErr(code int) error {
	return &SDKErr{Code: code}
}

type ChatData struct {
	Seq              int64  `json:"seq,omitempty"`
	MsgID            string `json:"msgid,omitempty"`
	PublicKeyVer     int32  `json:"publickey_ver,omitempty"`
	ChatID           string `json:"chat_id,omitempty"`
	EncryptRandomKey string `json:"encrypt_random_key"`
	EncryptChatMsg   string `json:"encrypt_chat_msg"`
}

type MediaData struct {
	OutIndexBuf string `json:"outindexbuf,omitempty"`
	IsFinish    bool   `json:"is_finish,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type Error struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (e *Error) Error() string {
	return "Error code: " + strconv.Itoa(e.ErrCode) + ", message: " + e.ErrMsg
}

type ChatRawData struct {
	Error
	ChatDataList []ChatData `json:"chatdata"`
}

func (s *SDK) GetChatData(seq, limit int64) ([]ChatData, error) {
	if s == nil {
		return nil, NewSDKErr(10002)
	}
	buf, err := s.getChatDataRaw(seq, limit)
	if err != nil {
		return nil, err
	}

	var data ChatRawData
	if err := json.Unmarshal(buf, &data); err != nil {
		return nil, err
	}
	if data.ErrCode != 0 {
		return nil, NewSDKErr(data.ErrCode)
	}
	return data.ChatDataList, nil
}

func (s *SDK) GetChatDataWithDecrypt(seq, limit int64) ([][]byte, error) {
	if s == nil {
		return nil, NewSDKErr(10002)
	}
	chatData, err := s.GetChatData(seq, limit)
	if err != nil {
		return nil, err
	}
	if len(chatData) == 0 {
		return nil, nil
	}
	out := make([][]byte, 0, len(chatData))
	for i := range chatData {
		plain, err := s.DecryptData(&chatData[i])
		if err != nil {
			return nil, err
		}
		out = append(out, plain)
	}
	return out, nil
}

func (s *SDK) DecryptData(cd *ChatData) ([]byte, error) {
	if s == nil {
		return nil, NewSDKErr(10002)
	}
	encryptKey, err := RSADecryptBase64(s.privateKey, cd.EncryptRandomKey)
	if err != nil {
		return nil, err
	}
	buf, err := s.decryptRaw(string(encryptKey), cd.EncryptChatMsg)
	if err != nil {
		return nil, err
	}

	// handle illegal escape character in text
	for i := 0; i < len(buf); {
		if buf[i] < 0x20 {
			buf = append(buf[:i], buf[i+1:]...)
			continue
		}
		i++
	}

	return buf, nil
}

func (s *SDK) GetMediaData(indexBuf string, sdkFileID string) (*MediaData, error) {
	if s == nil {
		return nil, NewSDKErr(10002)
	}
	return s.getMediaDataRaw(indexBuf, sdkFileID)
}

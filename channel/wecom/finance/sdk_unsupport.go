//go:build !linux || !cgo

package finance

import "errors"

var errUnsupported = errors.New("wecom finance sdk requires linux+cgo")

func NewSDK(corpID, corpSecret, rsaPrivateKey, proxy, passwd string, timeout int) (*SDK, error) {
	return nil, errUnsupported
}

func (s *SDK) Free() {
}

func (s *SDK) getChatDataRaw(int64, int64) ([]byte, error) {
	return nil, errUnsupported
}

func (s *SDK) decryptRaw(string, string) ([]byte, error) {
	return nil, errUnsupported
}

func (s *SDK) getMediaDataRaw(string, string) (*MediaData, error) {
	return nil, errUnsupported
}

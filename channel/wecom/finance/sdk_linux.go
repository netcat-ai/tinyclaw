//go:build linux && cgo

package finance

// #cgo LDFLAGS: -L${SRCDIR}/lib -lWeWorkFinanceSdk_C
// #cgo CFLAGS: -I ./lib/
// #include <stdlib.h>
// #include "WeWorkFinanceSdk_C.h"
import "C"
import "unsafe"

func NewSDK(corpId, corpSecret, rsaPrivateKey, proxy, passwd string, timeout int) (*SDK, error) {
	ptr := C.NewSdk()
	corpIdC := C.CString(corpId)
	corpSecretC := C.CString(corpSecret)
	defer func() {
		C.free(unsafe.Pointer(corpIdC))
		C.free(unsafe.Pointer(corpSecretC))
	}()
	if ret := int(C.Init(ptr, corpIdC, corpSecretC)); ret != 0 {
		C.DestroySdk(ptr)
		return nil, NewSDKErr(ret)
	}
	return &SDK{
		ptr:        unsafe.Pointer(ptr),
		privateKey: rsaPrivateKey,
		proxy:      unsafe.Pointer(C.CString(proxy)),
		passwd:     unsafe.Pointer(C.CString(passwd)),
		timeout:    timeout,
	}, nil
}

func (s *SDK) Free() {
	C.DestroySdk(s.cptr())
	C.free(s.proxy)
	C.free(s.passwd)
}

func (s *SDK) getChatDataRaw(seq int64, limit int64) ([]byte, error) {
	chatSlice := C.NewSlice()
	defer C.FreeSlice(chatSlice)

	proxy := (*C.char)(s.proxy)
	passwd := (*C.char)(s.passwd)
	ret := int(C.GetChatData(s.cptr(), C.ulonglong(seq), C.uint(limit), proxy, passwd, C.int(s.timeout), chatSlice))
	if ret != 0 {
		return nil, NewSDKErr(ret)
	}
	return getBytesFromSlice(chatSlice), nil
}

func getBytesFromSlice(s *C.struct_Slice_t) []byte {
	return C.GoBytes(unsafe.Pointer(C.GetContentFromSlice(s)), C.int(C.GetSliceLen(s)))
}

func (s *SDK) decryptRaw(encryptKey string, encryptMsg string) ([]byte, error) {
	encryptKeyC := C.CString(encryptKey)
	encryptMsgC := C.CString(encryptMsg)
	msgSlice := C.NewSlice()
	defer func() {
		C.free(unsafe.Pointer(encryptKeyC))
		C.free(unsafe.Pointer(encryptMsgC))
		C.FreeSlice(msgSlice)
	}()

	ret := int(C.DecryptData(encryptKeyC, encryptMsgC, msgSlice))
	if ret != 0 {
		return nil, NewSDKErr(ret)
	}
	return getBytesFromSlice(msgSlice), nil
}

func (s *SDK) getMediaDataRaw(indexBuf string, sdkFileId string) (*MediaData, error) {
	if s.ptr == nil {
		return nil, NewSDKErr(10002)
	}
	indexBufC := C.CString(indexBuf)
	sdkFileIdC := C.CString(sdkFileId)
	mediaDataC := C.NewMediaData()
	defer func() {
		C.free(unsafe.Pointer(indexBufC))
		C.free(unsafe.Pointer(sdkFileIdC))
		C.FreeMediaData(mediaDataC)
	}()

	proxy := (*C.char)(s.proxy)
	passwd := (*C.char)(s.passwd)
	ret := int(C.GetMediaData(s.cptr(), indexBufC, sdkFileIdC, proxy, passwd, C.int(s.timeout), mediaDataC))
	if ret != 0 {
		return nil, NewSDKErr(ret)
	}
	return &MediaData{
		OutIndexBuf: C.GoString(C.GetOutIndexBuf(mediaDataC)),
		Data:        C.GoBytes(unsafe.Pointer(C.GetData(mediaDataC)), C.int(C.GetDataLen(mediaDataC))),
		IsFinish:    int(C.IsMediaDataFinish(mediaDataC)) == 1,
	}, nil
}

func (s *SDK) cptr() *C.WeWorkFinanceSdk_t {
	return (*C.WeWorkFinanceSdk_t)(s.ptr)
}

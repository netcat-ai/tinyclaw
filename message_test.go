package main

import "testing"

func TestExtractWeComMessageText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "text message",
			raw:  `{"msgtype":"text","text":{"content":"hello"}}`,
			want: "hello",
		},
		{
			name: "markdown message",
			raw:  `{"msgtype":"markdown","markdown":{"content":"**hi**"}}`,
			want: "**hi**",
		},
		{
			name: "image message falls back to url",
			raw:  `{"msgtype":"image","image":{"url":"https://example.test/a.png"}}`,
			want: "https://example.test/a.png",
		},
		{
			name: "unknown payload falls back to msgtype",
			raw:  `{"msgtype":"voice"}`,
			want: "[voice]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractWeComMessageText(tc.raw)
			if err != nil {
				t.Fatalf("extractWeComMessageText returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("extractWeComMessageText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractWeComMessageText_InvalidPayload(t *testing.T) {
	_, err := extractWeComMessageText(`{"msgtype":"text"`)
	if err == nil {
		t.Fatal("extractWeComMessageText error = nil, want non-nil")
	}
}

func TestExtractWeComImageSDKFileID(t *testing.T) {
	got, ok, err := extractWeComImageSDKFileID(`{"msgtype":"image","image":{"sdkfileid":"sdk-file-1"}}`)
	if err != nil {
		t.Fatalf("extractWeComImageSDKFileID error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "sdk-file-1" {
		t.Fatalf("sdk file id = %q, want sdk-file-1", got)
	}
}

func TestExtractWeComImageSDKFileID_NonImageMessage(t *testing.T) {
	got, ok, err := extractWeComImageSDKFileID(`{"msgtype":"text","text":{"content":"hello"}}`)
	if err != nil {
		t.Fatalf("extractWeComImageSDKFileID error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if got != "" {
		t.Fatalf("sdk file id = %q, want empty", got)
	}
}

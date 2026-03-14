package protocol

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestWriteReadFrame_RoundTrip(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	want := []byte(`{"token":"test-secret"}`)

	go func() {
		if err := WriteFrame(conn1, MsgTypeAuth, want); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
	}()

	msgType, payload, err := ReadFrame(conn2)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgTypeAuth {
		t.Errorf("msgType = 0x%02x, want 0x%02x", msgType, MsgTypeAuth)
	}
	if string(payload) != string(want) {
		t.Errorf("payload = %q, want %q", payload, want)
	}
}

func TestWriteReadFrame_EmptyPayload(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	go func() {
		if err := WriteFrame(conn1, MsgTypePing, []byte{}); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
	}()

	msgType, payload, err := ReadFrame(conn2)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgTypePing {
		t.Errorf("msgType = 0x%02x, want 0x%02x", msgType, MsgTypePing)
	}
	if len(payload) != 0 {
		t.Errorf("payload length = %d, want 0", len(payload))
	}
}

func TestWriteReadFrame_LargePayload(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	// 上限ギリギリのペイロード (MaxPayloadSize)
	want := make([]byte, MaxPayloadSize)
	for i := range want {
		want[i] = byte(i % 256)
	}

	go func() {
		if err := WriteFrame(conn1, MsgTypeAuth, want); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
	}()

	msgType, payload, err := ReadFrame(conn2)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgTypeAuth {
		t.Errorf("msgType = 0x%02x, want 0x%02x", msgType, MsgTypeAuth)
	}
	if len(payload) != MaxPayloadSize {
		t.Errorf("payload length = %d, want %d", len(payload), MaxPayloadSize)
	}
}

func TestReadFrame_PayloadTooLarge(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	// MaxPayloadSize + 1 のサイズをヘッダーに書き込む
	go func() {
		header := []byte{MsgTypeAuth, 0, 0x10, 0, 1} // 1<<20 + 1 = 1048577
		conn1.Write(header)
	}()

	_, _, err := ReadFrame(conn2)
	if err == nil {
		t.Fatal("expected error for payload too large, got nil")
	}
	if !strings.Contains(err.Error(), "payload too large") {
		t.Errorf("error = %v, want payload too large error", err)
	}
}

func TestWriteReadMessage_Auth(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	want := &MsgAuth{Token: "my-secret-token"}

	go func() {
		if err := WriteMessage(conn1, MsgTypeAuth, want); err != nil {
			t.Errorf("WriteMessage: %v", err)
		}
	}()

	msgType, payload, err := ReadMessage(conn2)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msgType != MsgTypeAuth {
		t.Errorf("msgType = 0x%02x, want 0x%02x", msgType, MsgTypeAuth)
	}

	var got MsgAuth
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Token != want.Token {
		t.Errorf("token = %q, want %q", got.Token, want.Token)
	}
}

func TestMsgAuth_Validate(t *testing.T) {
	tests := []struct {
		name    string
		msg     MsgAuth
		wantErr bool
	}{
		{"valid token", MsgAuth{Token: "abc"}, false},
		{"empty token", MsgAuth{Token: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestUnmarshalMessage(t *testing.T) {
	tests := []struct {
		name    string
		msgType byte
		data    []byte
		wantErr bool
	}{
		{"auth", MsgTypeAuth, []byte(`{"token":"x"}`), false},
		{"auth resp", MsgTypeAuthResp, []byte(`{"success":true,"message":"ok"}`), false},
		{"ping (no payload)", MsgTypePing, nil, false},
		{"unknown type", 0xFF, []byte(`{}`), true},
		{"invalid json", MsgTypeAuth, []byte(`not-json`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalMessage(tt.msgType, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalMessage() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

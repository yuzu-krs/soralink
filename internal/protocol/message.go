package protocol

import (
	"encoding/json"
	"fmt"
)

// メッセージタイプ定数
const (
	MsgTypeAuth          byte = 0x01
	MsgTypeAuthResp      byte = 0x02
	MsgTypeRequestTunnel byte = 0x03
	MsgTypeTunnelReady   byte = 0x04
	MsgTypeNewConnection byte = 0x05
	MsgTypePing          byte = 0x06
	MsgTypePong          byte = 0x07
	MsgTypeError         byte = 0x08
	MsgTypeCloseTunnel   byte = 0x09
	MsgTypeDataConnInit  byte = 0x0A
)

// MsgAuth はクライアントからサーバーへの認証メッセージ
type MsgAuth struct {
	Token string `json:"token"`
}

func (m *MsgAuth) Validate() error {
	if m.Token == "" {
		return ErrEmptyToken
	}
	return nil
}

// MsgAuthResp はサーバーからクライアントへの認証応答
type MsgAuthResp struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// MsgRequestTunnel はクライアントからサーバーへのトンネル作成要求
type MsgRequestTunnel struct {
	LocalPort     int    `json:"local_port"`
	Protocol      string `json:"protocol"`
	RequestedPort int    `json:"requested_port"`
}

// MsgTunnelReady はサーバーからクライアントへのトンネル準備完了通知
type MsgTunnelReady struct {
	TunnelID   string `json:"tunnel_id"`
	RemotePort int    `json:"remote_port"`
	URL        string `json:"url"`
}

// MsgNewConnection はサーバーからクライアントへの新規接続通知
type MsgNewConnection struct {
	TunnelID     string `json:"tunnel_id"`
	ConnectionID string `json:"connection_id"`
}

// MsgDataConnInit はクライアントからサーバーへのデータコネクション初期化
type MsgDataConnInit struct {
	ConnectionID string `json:"connection_id"`
}

// MsgError はエラー通知メッセージ（双方向）
type MsgError struct {
	Message string `json:"message"`
}

// MarshalMessage は構造体を JSON バイト列に変換する
func MarshalMessage(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return data, nil
}

// UnmarshalMessage はメッセージタイプに基づいて JSON バイト列を構造体にデコードする
func UnmarshalMessage(msgType byte, data []byte) (any, error) {
	var v any
	switch msgType {
	case MsgTypeAuth:
		v = &MsgAuth{}
	case MsgTypeAuthResp:
		v = &MsgAuthResp{}
	case MsgTypeRequestTunnel:
		v = &MsgRequestTunnel{}
	case MsgTypeTunnelReady:
		v = &MsgTunnelReady{}
	case MsgTypeNewConnection:
		v = &MsgNewConnection{}
	case MsgTypeDataConnInit:
		v = &MsgDataConnInit{}
	case MsgTypeError:
		v = &MsgError{}
	case MsgTypePing, MsgTypePong:
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown message type 0x%02x: %w", msgType, ErrInvalidMessage)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("unmarshal message type 0x%02x: %w", msgType, ErrInvalidMessage)
	}
	return v, nil
}

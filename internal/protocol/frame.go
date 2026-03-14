package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// MaxPayloadSize はフレームペイロードの最大サイズ (1MB)
const MaxPayloadSize = 1 << 20

const frameHeaderSize = 5 // Type(1B) + Length(4B)

// WriteFrame はフレームを conn に書き込む
func WriteFrame(conn net.Conn, msgType byte, payload []byte) error {
	header := make([]byte, frameHeaderSize)
	header[0] = msgType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))

	if _, err := conn.Write(header); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := conn.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame はフレームを conn から読み取る
func ReadFrame(conn net.Conn) (msgType byte, payload []byte, err error) {
	var header [frameHeaderSize]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return 0, nil, fmt.Errorf("read frame header: %w", err)
	}

	msgType = header[0]
	length := binary.BigEndian.Uint32(header[1:5])

	if length > MaxPayloadSize {
		return 0, nil, fmt.Errorf("payload too large (%d bytes): %w", length, ErrPayloadTooLarge)
	}

	payload = make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, nil, fmt.Errorf("read frame payload: %w", err)
		}
	}
	return msgType, payload, nil
}

// WriteMessage は構造体を JSON エンコードしてフレームとして送信する
func WriteMessage(conn net.Conn, msgType byte, v any) error {
	payload, err := MarshalMessage(v)
	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return WriteFrame(conn, msgType, payload)
}

// ReadMessage はフレームを読み取って返す（ReadFrame のエイリアス）
func ReadMessage(conn net.Conn) (msgType byte, payload []byte, err error) {
	return ReadFrame(conn)
}

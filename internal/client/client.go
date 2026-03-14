package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/yuzut/soralink/internal/protocol"
)

// Client は soralink クライアント
type Client struct {
	cfg      *Config
	logger   *slog.Logger
	ctrlConn net.Conn
	tunnels  map[string]int // tunnel_id → local_port
}

// NewClient はクライアントを初期化する
func NewClient(cfg *Config, logger *slog.Logger) *Client {
	return &Client{
		cfg:     cfg,
		logger:  logger,
		tunnels: make(map[string]int),
	}
}

// Run はサーバーに接続し、トンネルを確立してメッセージループに入る
func (c *Client) Run(ctx context.Context) error {
	if err := c.connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.ctrlConn.Close()

	if err := c.authenticate(); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	if err := c.requestTunnels(); err != nil {
		return fmt.Errorf("request tunnels: %w", err)
	}

	return c.messageLoop(ctx)
}

// connect はサーバーに TCP 接続する
func (c *Client) connect(ctx context.Context) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.cfg.ServerAddr, err)
	}
	c.ctrlConn = conn
	c.logger.Info("connected to server", "addr", c.cfg.ServerAddr)
	return nil
}

// authenticate はサーバーに認証メッセージを送り、応答を確認する
func (c *Client) authenticate() error {
	msg := &protocol.MsgAuth{Token: c.cfg.AuthToken}
	if err := protocol.WriteMessage(c.ctrlConn, protocol.MsgTypeAuth, msg); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	c.ctrlConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	msgType, payload, err := protocol.ReadFrame(c.ctrlConn)
	c.ctrlConn.SetReadDeadline(time.Time{})

	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if msgType != protocol.MsgTypeAuthResp {
		return fmt.Errorf("unexpected response type: 0x%02x", msgType)
	}

	var resp protocol.MsgAuthResp
	if err := json.Unmarshal(payload, &resp); err != nil {
		return fmt.Errorf("unmarshal auth response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("authentication failed: %s", resp.Message)
	}

	c.logger.Info("authenticated successfully")
	return nil
}

// requestTunnels は設定された全トンネルの作成を要求する
func (c *Client) requestTunnels() error {
	for _, tc := range c.cfg.Tunnels {
		req := &protocol.MsgRequestTunnel{
			LocalPort:     tc.LocalPort,
			Protocol:      tc.Protocol,
			RequestedPort: tc.RemotePort,
		}
		if err := protocol.WriteMessage(c.ctrlConn, protocol.MsgTypeRequestTunnel, req); err != nil {
			return fmt.Errorf("send tunnel request: %w", err)
		}

		c.ctrlConn.SetReadDeadline(time.Now().Add(10 * time.Second))
		msgType, payload, err := protocol.ReadFrame(c.ctrlConn)
		c.ctrlConn.SetReadDeadline(time.Time{})

		if err != nil {
			return fmt.Errorf("read tunnel response: %w", err)
		}

		switch msgType {
		case protocol.MsgTypeTunnelReady:
			var resp protocol.MsgTunnelReady
			if err := json.Unmarshal(payload, &resp); err != nil {
				return fmt.Errorf("unmarshal tunnel ready: %w", err)
			}
			c.tunnels[resp.TunnelID] = tc.LocalPort
			c.logger.Info("tunnel established",
				"remote_port", resp.RemotePort,
				"url", resp.URL,
				"local_port", tc.LocalPort,
			)
		case protocol.MsgTypeError:
			var errMsg protocol.MsgError
			if err := json.Unmarshal(payload, &errMsg); err != nil {
				return fmt.Errorf("unmarshal error: %w", err)
			}
			return fmt.Errorf("tunnel request rejected: %s", errMsg.Message)
		default:
			return fmt.Errorf("unexpected response type: 0x%02x", msgType)
		}
	}
	return nil
}

// messageLoop は制御コネクションのメッセージを受信し続ける
func (c *Client) messageLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.ctrlConn.SetReadDeadline(time.Now().Add(90 * time.Second))
		msgType, payload, err := protocol.ReadFrame(c.ctrlConn)
		c.ctrlConn.SetReadDeadline(time.Time{})

		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read control message: %w", err)
		}

		switch msgType {
		case protocol.MsgTypeNewConnection:
			var msg protocol.MsgNewConnection
			if err := json.Unmarshal(payload, &msg); err != nil {
				c.logger.Warn("invalid new connection message", "err", err)
				continue
			}
			go c.handleNewConnection(ctx, &msg) // goroutine: handle new external connection
		case protocol.MsgTypePing:
			if err := protocol.WriteFrame(c.ctrlConn, protocol.MsgTypePong, nil); err != nil {
				c.logger.Debug("failed to send pong", "err", err)
				return fmt.Errorf("send pong: %w", err)
			}
		case protocol.MsgTypeError:
			var errMsg protocol.MsgError
			if err := json.Unmarshal(payload, &errMsg); err == nil {
				c.logger.Error("server error", "message", errMsg.Message)
			}
		default:
			c.logger.Debug("unknown message type", "type", msgType)
		}
	}
}

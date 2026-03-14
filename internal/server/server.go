package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/yuzut/soralink/internal/protocol"
)

// pendingConn はデータコネクションのマッチング待ちを表す
type pendingConn struct {
	ch      chan net.Conn
	created time.Time
}

// Server は soralink の制御サーバー
type Server struct {
	cfg      *Config
	logger   *slog.Logger
	listener net.Listener
	tunnels  *TunnelManager
	pending  sync.Map
}

// NewServer はサーバーを初期化する
func NewServer(cfg *Config, logger *slog.Logger) *Server {
	min, max := cfg.EffectivePortRange()
	return &Server{
		cfg:     cfg,
		logger:  logger,
		tunnels: NewTunnelManager(min, max),
	}
}

// Run はサーバーを起動し、制御ポートで待ち受ける
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.cfg.EffectiveControlPort())
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	s.listener = ln
	s.logger.Info("server started", "control_port", s.cfg.EffectiveControlPort())

	go func() { // goroutine: wait for context cancellation to close listener
		<-ctx.Done()
		ln.Close()
	}()

	return s.acceptLoop(ctx)
}

// acceptLoop は接続を受け付けて振り分ける
func (s *Server) acceptLoop(ctx context.Context) error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				s.logger.Warn("accept error", "err", err)
				continue
			}
		}

		go func() { // goroutine: handle incoming connection
			defer func() {
				if r := recover(); r != nil {
					s.logger.Error("panic in connection handler", "recover", r)
				}
			}()
			s.handleConn(ctx, conn)
		}()
	}
}

// handleConn は最初のフレームを読んで制御コネクションかデータコネクションかを判別する
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	msgType, payload, err := protocol.ReadFrame(conn)
	conn.SetReadDeadline(time.Time{})

	if err != nil {
		s.logger.Debug("failed to read initial frame", "remote", conn.RemoteAddr(), "err", err)
		conn.Close()
		return
	}

	switch msgType {
	case protocol.MsgTypeAuth:
		s.handleControlConn(ctx, conn, payload)
	case protocol.MsgTypeDataConnInit:
		s.handleDataConn(conn, payload)
	default:
		s.logger.Debug("unexpected initial message type", "type", msgType, "remote", conn.RemoteAddr())
		conn.Close()
	}
}

// handleControlConn は制御コネクションを処理する
func (s *Server) handleControlConn(ctx context.Context, conn net.Conn, authPayload []byte) {
	defer conn.Close()
	defer s.tunnels.RemoveByClient(conn)

	// 認証
	if err := s.authenticate(conn, authPayload); err != nil {
		s.logger.Debug("auth failed", "remote", conn.RemoteAddr(), "err", err)
		return
	}

	s.logger.Info("client authenticated", "remote", conn.RemoteAddr())

	// メッセージループ
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		msgType, payload, err := protocol.ReadFrame(conn)
		conn.SetReadDeadline(time.Time{})

		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Debug("control connection read error", "remote", conn.RemoteAddr(), "err", err)
			}
			return
		}

		switch msgType {
		case protocol.MsgTypeRequestTunnel:
			s.handleTunnelRequest(conn, payload)
		case protocol.MsgTypePing:
			if err := protocol.WriteFrame(conn, protocol.MsgTypePong, nil); err != nil {
				s.logger.Debug("failed to send pong", "err", err)
				return
			}
		case protocol.MsgTypeCloseTunnel:
			s.handleCloseTunnel(payload)
		default:
			s.logger.Debug("unknown message type in control loop", "type", msgType)
		}
	}
}

// authenticate はトークンを照合し、結果を返送する
func (s *Server) authenticate(conn net.Conn, payload []byte) error {
	var msg protocol.MsgAuth
	if err := json.Unmarshal(payload, &msg); err != nil {
		resp := &protocol.MsgAuthResp{Success: false, Message: "invalid auth message"}
		protocol.WriteMessage(conn, protocol.MsgTypeAuthResp, resp)
		return fmt.Errorf("unmarshal auth: %w", err)
	}

	if err := msg.Validate(); err != nil {
		resp := &protocol.MsgAuthResp{Success: false, Message: err.Error()}
		protocol.WriteMessage(conn, protocol.MsgTypeAuthResp, resp)
		return fmt.Errorf("validate auth: %w", err)
	}

	if msg.Token != s.cfg.AuthToken {
		resp := &protocol.MsgAuthResp{Success: false, Message: "invalid token"}
		protocol.WriteMessage(conn, protocol.MsgTypeAuthResp, resp)
		return protocol.ErrAuthFailed
	}

	resp := &protocol.MsgAuthResp{Success: true, Message: "authenticated"}
	if err := protocol.WriteMessage(conn, protocol.MsgTypeAuthResp, resp); err != nil {
		return fmt.Errorf("send auth response: %w", err)
	}
	return nil
}

// handleTunnelRequest はトンネル作成要求を処理する
func (s *Server) handleTunnelRequest(conn net.Conn, payload []byte) {
	var msg protocol.MsgRequestTunnel
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.logger.Warn("invalid tunnel request", "err", err)
		errMsg := &protocol.MsgError{Message: "invalid tunnel request"}
		protocol.WriteMessage(conn, protocol.MsgTypeError, errMsg)
		return
	}

	tunnel, err := s.tunnels.Create(conn, msg.RequestedPort, msg.LocalPort)
	if err != nil {
		s.logger.Warn("failed to create tunnel", "err", err)
		errMsg := &protocol.MsgError{Message: err.Error()}
		protocol.WriteMessage(conn, protocol.MsgTypeError, errMsg)
		return
	}

	resp := &protocol.MsgTunnelReady{
		TunnelID:   tunnel.ID,
		RemotePort: tunnel.RemotePort,
		URL:        fmt.Sprintf(":%d", tunnel.RemotePort),
	}
	if err := protocol.WriteMessage(conn, protocol.MsgTypeTunnelReady, resp); err != nil {
		s.logger.Warn("failed to send tunnel ready", "err", err)
		s.tunnels.Remove(tunnel.ID)
		return
	}

	go tunnel.AcceptLoop(s) // goroutine: accept external connections for tunnel
}

// handleCloseTunnel はトンネル閉鎖要求を処理する
func (s *Server) handleCloseTunnel(payload []byte) {
	var msg struct {
		TunnelID string `json:"tunnel_id"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.logger.Warn("invalid close tunnel request", "err", err)
		return
	}
	s.tunnels.Remove(msg.TunnelID)
}

// handleDataConn はデータコネクションを処理する
func (s *Server) handleDataConn(conn net.Conn, payload []byte) {
	var msg protocol.MsgDataConnInit
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.logger.Debug("invalid data conn init", "err", err)
		conn.Close()
		return
	}

	pcVal, ok := s.pending.Load(msg.ConnectionID)
	if !ok {
		s.logger.Debug("no pending connection for id", "connection_id", msg.ConnectionID)
		conn.Close()
		return
	}

	pc := pcVal.(*pendingConn)
	pc.ch <- conn
}

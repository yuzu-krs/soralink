package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/yuzut/soralink/internal/protocol"
)

// TunnelManager はトンネルの作成・取得・削除を管理する
type TunnelManager struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
	ports   map[int]bool
	minPort int
	maxPort int
}

// Tunnel は 1 つの公開トンネルを表す
type Tunnel struct {
	ID         string
	RemotePort int
	ClientConn net.Conn
	LocalPort  int
	listener   net.Listener
}

// NewTunnelManager は TunnelManager を初期化する
func NewTunnelManager(min, max int) *TunnelManager {
	return &TunnelManager{
		tunnels: make(map[string]*Tunnel),
		ports:   make(map[int]bool),
		minPort: min,
		maxPort: max,
	}
}

// Create はトンネルを作成し、公開ポートで Listen を開始する
func (m *TunnelManager) Create(clientConn net.Conn, requestedPort int, localPort int) (*Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	port, err := m.allocatePort(requestedPort)
	if err != nil {
		return nil, err
	}

	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		delete(m.ports, port)
		return nil, fmt.Errorf("listen on port %d: %w", port, err)
	}

	id := generateID()
	t := &Tunnel{
		ID:         id,
		RemotePort: port,
		ClientConn: clientConn,
		LocalPort:  localPort,
		listener:   ln,
	}
	m.tunnels[id] = t

	slog.Info("tunnel created", "tunnel_id", id, "remote_port", port, "local_port", localPort)
	return t, nil
}

// Get は ID でトンネルを取得する
func (m *TunnelManager) Get(id string) (*Tunnel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tunnels[id]
	return t, ok
}

// Remove はトンネルを閉じてリソースを解放する
func (m *TunnelManager) Remove(id string) {
	m.mu.Lock()
	t, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.tunnels, id)
	delete(m.ports, t.RemotePort)
	m.mu.Unlock()

	t.listener.Close()
	slog.Info("tunnel removed", "tunnel_id", id, "remote_port", t.RemotePort)
}

// RemoveByClient はクライアントコネクションに紐づく全トンネルを削除する
func (m *TunnelManager) RemoveByClient(conn net.Conn) {
	m.mu.Lock()
	var toRemove []string
	for id, t := range m.tunnels {
		if t.ClientConn == conn {
			toRemove = append(toRemove, id)
		}
	}
	for _, id := range toRemove {
		t := m.tunnels[id]
		delete(m.tunnels, id)
		delete(m.ports, t.RemotePort)
		t.listener.Close()
		slog.Info("tunnel removed (client disconnect)", "tunnel_id", id, "remote_port", t.RemotePort)
	}
	m.mu.Unlock()
}

// allocatePort はポートを確保する (mutex は呼び出し元で取得済み前提)
func (m *TunnelManager) allocatePort(requested int) (int, error) {
	if requested > 0 {
		if requested < m.minPort || requested > m.maxPort {
			return 0, fmt.Errorf("requested port %d is outside range [%d, %d]", requested, m.minPort, m.maxPort)
		}
		if m.ports[requested] {
			return 0, fmt.Errorf("requested port %d is already in use", requested)
		}
		m.ports[requested] = true
		return requested, nil
	}

	for p := m.minPort; p <= m.maxPort; p++ {
		if !m.ports[p] {
			m.ports[p] = true
			return p, nil
		}
	}
	return 0, protocol.ErrPortExhausted
}

// AcceptLoop は外部接続を受け付けてサーバーにマッチングを依頼する
func (t *Tunnel) AcceptLoop(server *Server) {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			slog.Debug("tunnel accept loop ended", "tunnel_id", t.ID, "err", err)
			return
		}

		connID := generateID()
		pc := &pendingConn{
			ch:      make(chan net.Conn, 1),
			created: time.Now(),
		}
		server.pending.Store(connID, pc)

		// クライアントに新規接続を通知
		msg := &protocol.MsgNewConnection{
			TunnelID:     t.ID,
			ConnectionID: connID,
		}
		if err := protocol.WriteMessage(t.ClientConn, protocol.MsgTypeNewConnection, msg); err != nil {
			slog.Warn("failed to notify client of new connection", "tunnel_id", t.ID, "err", err)
			conn.Close()
			server.pending.Delete(connID)
			continue
		}

		go func(extConn net.Conn, cID string) { // goroutine: wait for data connection and bridge
			defer func() {
				server.pending.Delete(cID)
			}()

			pcVal, ok := server.pending.Load(cID)
			if !ok {
				extConn.Close()
				return
			}
			p := pcVal.(*pendingConn)

			select {
			case dataConn := <-p.ch:
				result := Bridge(extConn, dataConn)
				slog.Info("connection completed",
					"tunnel_id", t.ID,
					"connection_id", cID,
					"bytes_out", result.BytesOut,
					"bytes_in", result.BytesIn,
					"duration", result.Duration,
				)
			case <-time.After(10 * time.Second):
				slog.Warn("data connection timeout", "connection_id", cID)
				extConn.Close()
			}
		}(conn, connID)
	}
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

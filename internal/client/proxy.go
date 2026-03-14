package client

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/yuzut/soralink/internal/protocol"
)

// handleNewConnection はサーバーからの新規接続通知を処理し、データコネクションを確立してブリッジする
func (c *Client) handleNewConnection(ctx context.Context, msg *protocol.MsgNewConnection) {
	localPort, ok := c.tunnels[msg.TunnelID]
	if !ok {
		c.logger.Warn("received connection for unknown tunnel", "tunnel_id", msg.TunnelID)
		return
	}

	// サーバーにデータコネクションを確立
	dialer := net.Dialer{Timeout: 10 * time.Second}
	serverConn, err := dialer.DialContext(ctx, "tcp", c.cfg.ServerAddr)
	if err != nil {
		c.logger.Error("failed to establish data connection to server", "err", err)
		return
	}

	// データコネクション初期化メッセージを送信
	initMsg := &protocol.MsgDataConnInit{ConnectionID: msg.ConnectionID}
	if err := protocol.WriteMessage(serverConn, protocol.MsgTypeDataConnInit, initMsg); err != nil {
		c.logger.Error("failed to send data conn init", "err", err)
		serverConn.Close()
		return
	}

	// ローカルサービスに接続
	localAddr := fmt.Sprintf("localhost:%d", localPort)
	localConn, err := dialer.DialContext(ctx, "tcp", localAddr)
	if err != nil {
		c.logger.Error("failed to connect to local service", "addr", localAddr, "err", err)
		serverConn.Close()
		return
	}

	c.logger.Debug("bridging connection", "connection_id", msg.ConnectionID, "local_port", localPort)

	// 双方向ブリッジ
	bridge(serverConn, localConn)
}

// bridge は 2 つの接続を双方向にコピーする
func bridge(conn1, conn2 net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src net.Conn) { // goroutine: bridge one direction
		defer wg.Done()
		defer dst.Close()
		_, err := io.Copy(dst, src)
		if err != nil {
			slog.Debug("bridge copy ended", "err", err)
		}
	}

	go cp(conn1, conn2) // goroutine: bridge server → local
	go cp(conn2, conn1) // goroutine: bridge local → server
	wg.Wait()
}

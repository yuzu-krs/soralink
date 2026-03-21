package server

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// BridgeResult はブリッジ完了後の統計情報
type BridgeResult struct {
	BytesOut int64         // conn1 → conn2 に転送されたバイト数
	BytesIn  int64         // conn2 → conn1 に転送されたバイト数
	Duration time.Duration // ブリッジの稼働時間
}

// countingWriter は書き込みバイト数を記録する io.Writer ラッパー
type countingWriter struct {
	w     io.Writer
	bytes atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.bytes.Add(int64(n))
	return n, err
}

// Bridge は 2 つの net.Conn を双方向にコピーする。
// どちらか一方が閉じたらもう一方も閉じる。
func Bridge(conn1, conn2 net.Conn) BridgeResult {
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)

	cwOut := &countingWriter{w: conn2} // conn1 → conn2
	cwIn := &countingWriter{w: conn1}  // conn2 → conn1

	cp := func(dst *countingWriter, src, dstConn net.Conn) { // goroutine: bridge one direction
		defer wg.Done()
		defer dstConn.Close()
		io.Copy(dst, src) //nolint:errcheck // EOF は正常
	}

	go cp(cwOut, conn1, conn2) // goroutine: bridge direction conn1→conn2
	go cp(cwIn, conn2, conn1)  // goroutine: bridge direction conn2→conn1
	wg.Wait()

	return BridgeResult{
		BytesOut: cwOut.bytes.Load(),
		BytesIn:  cwIn.bytes.Load(),
		Duration: time.Since(start),
	}
}

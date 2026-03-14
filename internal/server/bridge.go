package server

import (
	"io"
	"net"
	"sync"
)

// Bridge は 2 つの net.Conn を双方向にコピーする。
// どちらか一方が閉じたらもう一方も閉じる。
func Bridge(conn1, conn2 net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src net.Conn) { // goroutine: bridge one direction
		defer wg.Done()
		defer dst.Close()
		io.Copy(dst, src) //nolint:errcheck // EOF は正常
	}

	go cp(conn1, conn2) // goroutine: bridge direction 1→2
	go cp(conn2, conn1) // goroutine: bridge direction 2→1
	wg.Wait()
}

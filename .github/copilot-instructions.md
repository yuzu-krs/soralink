# Soralink — GitHub Copilot カスタム指示書

このファイルは Copilot がコードを生成・提案する際に従うべきプロジェクト固有のルールを定義します。

---

## プロジェクト概要

- **言語**: Go 1.22+
- **用途**: セルフホスト型 TCP リレーサーバー（ngrok / playit.gg 相当）
- **構成**: `soralink-server`（VPS 上で動作）と `soralink-client`（ローカルで動作）の 2 バイナリ
- **内部通信**: TCP + 独自バイナリフレーム（Type 1B + Len 4B + JSON Payload）

---

## 設計思想

### 基本原則

> **"明示的であれ、暗黙的であるな"**

Soralink は長時間稼働するネットワークデーモンです。すべての設計判断は **障害耐性・可観測性・保守性** の 3 つを軸に行います。

#### 1. 障害は正常系の一部

ネットワークプログラミングにおいて接続切断・タイムアウト・半開き状態は **必ず起こる** 前提で設計します。

```go
// ✅ Good: 接続切断を想定した正常系
func (s *Server) handleClient(ctx context.Context, conn net.Conn) {
    defer conn.Close()

    if err := s.authenticate(ctx, conn); err != nil {
        slog.Debug("auth failed", "remote", conn.RemoteAddr(), "err", err)
        return // エラーログして戻るだけ。panic も Fatal もしない
    }
    // ...
}
```

```go
// ❌ Bad: ネットワークエラーを致命的として扱う
func (s *Server) handleClient(ctx context.Context, conn net.Conn) {
    if err := s.authenticate(ctx, conn); err != nil {
        log.Fatalf("auth error: %v", err) // サーバー全体が落ちる
    }
}
```

#### 2. ゼロ値は有効な状態

Go のゼロ値を活用し、構造体が `New` なしでも壊れない設計を心がけます。ただし、必須の依存がある場合はコンストラクタを強制します。

```go
// ✅ Good: ゼロ値で安全に使える補助構造体
type PortRange struct {
    Min int // 0 ならデフォルト 10000
    Max int // 0 ならデフォルト 20000
}

func (r PortRange) Effective() (int, int) {
    min, max := r.Min, r.Max
    if min == 0 { min = 10000 }
    if max == 0 { max = 20000 }
    return min, max
}
```

```go
// ❌ Bad: ゼロ値を使うとパニック
type PortRange struct {
    ports []int // nil のまま使うと index out of range
}
```

#### 3. 小さいインターフェース、大きい構造体

インターフェースは **消費側** で定義し、1〜2 メソッドに留めます。大きなインターフェースは避けます。

```go
// ✅ Good: 必要最小限のインターフェース（消費側で定義）
// internal/server/tunnel.go
type PortAllocator interface {
    Allocate() (int, error)
    Release(port int)
}
```

```go
// ❌ Bad: "何でもできる" 巨大インターフェース
type TunnelService interface {
    Allocate() (int, error)
    Release(port int)
    CreateTunnel(cfg TunnelConfig) (*Tunnel, error)
    CloseTunnel(id string) error
    ListTunnels() []*Tunnel
    GetStats() *Stats
    SetLogger(l *slog.Logger)
    // ... 10 個以上のメソッド
}
```

#### 4. レイヤー分離の徹底

各レイヤーは **自分の責務だけ** を持ちます。レイヤーを跨ぐ知識の漏洩を禁止します。

```
┌─────────────────────────────┐
│  cmd/ (起動・設定読み込み)    │  ← main() のみ。ロジック皆無
├─────────────────────────────┤
│  internal/server or client  │  ← ビジネスロジック
├─────────────────────────────┤
│  internal/protocol          │  ← フレーム変換・メッセージ定義
├─────────────────────────────┤
│  net.Conn (標準ライブラリ)   │  ← トランスポート層
└─────────────────────────────┘
```

```go
// ✅ Good: server 層は protocol の型だけを知っている
func (s *Server) handleTunnelRequest(conn net.Conn, msg *protocol.MsgRequestTunnel) error {
    port, err := s.alloc.Allocate()
    if err != nil {
        return fmt.Errorf("allocate port: %w", err)
    }
    resp := &protocol.MsgTunnelReady{
        TunnelID:   generateID(),
        RemotePort: port,
    }
    return protocol.WriteMessage(conn, protocol.MsgTypeTunnelReady, resp)
}
```

```go
// ❌ Bad: server 層がフレームのバイナリ構造を直接触る
func (s *Server) handleTunnelRequest(conn net.Conn, raw []byte) error {
    header := make([]byte, 5)
    header[0] = 0x04 // マジックナンバー直書き
    binary.BigEndian.PutUint32(header[1:5], uint32(len(raw)))
    conn.Write(header) // protocol 層の責務を侵害
}
```

#### 5. 設定は外から、ロジックは中から

設定値はすべて外部（YAML / CLI フラグ）から注入し、コード内にデフォルト以外のハードコードを置きません。

```go
// ✅ Good: 設定構造体で一元管理
type ServerConfig struct {
    ControlPort int       `yaml:"control_port"`
    AuthToken   string    `yaml:"auth_token"`
    PortRange   PortRange `yaml:"port_range"`
    LogLevel    string    `yaml:"log_level"`
}
```

```go
// ❌ Bad: コードの奥にマジックナンバー
func (s *Server) Run(ctx context.Context) error {
    ln, err := net.Listen("tcp", ":4610") // ハードコード
    // ...
    if port > 20000 { // ハードコード
        return ErrPortExhausted
    }
}
```

#### 6. Fail Fast, Recover Gracefully

起動時の設定エラーは即座に失敗させ、実行中のネットワークエラーは優雅にリカバリします。

```go
// ✅ Good: 起動時バリデーションで早期失敗
func main() {
    cfg, err := loadConfig(configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "config error: %v\n", err)
        os.Exit(1) // main() でだけ os.Exit を許可
    }
    if err := cfg.Validate(); err != nil {
        fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
        os.Exit(1)
    }
    // ...
}
```

```go
// ❌ Bad: 設定エラーを実行中に初めて検出
func (s *Server) handleClient(ctx context.Context, conn net.Conn) {
    if s.cfg.AuthToken == "" { // ここで気づくのは遅すぎる
        slog.Error("no auth token configured")
        return
    }
}
```

---

### ベストプラクティス集

#### コネクションライフサイクル管理

```go
// ✅ Best Practice: defer + 明示的な所有権
func (s *Server) acceptLoop(ctx context.Context) error {
    for {
        conn, err := s.listener.Accept()
        if err != nil {
            select {
            case <-ctx.Done():
                return ctx.Err()
            default:
                slog.Warn("accept error", "err", err)
                continue // 一時的なエラーはリトライ
            }
        }
        go func() { // goroutine: handle client session
            defer conn.Close() // この goroutine が接続の所有者
            s.handleClient(ctx, conn)
        }()
    }
}
```

```go
// ❌ Anti-Pattern: 接続の所有権が不明確
func (s *Server) acceptLoop(ctx context.Context) error {
    for {
        conn, _ := s.listener.Accept() // エラー無視
        go s.handleClient(ctx, conn)   // Close の責任が不明確
    }
}
```

#### リソースクリーンアップの連鎖

トンネル削除時は **逆順に** リソースを解放します。

```go
// ✅ Best Practice: 作成の逆順でクリーンアップ
func (t *Tunnel) Close() error {
    // 1. 新規接続の受付を停止
    t.listener.Close()

    // 2. 既存の全データコネクションを閉じる
    t.mu.Lock()
    for _, conn := range t.connections {
        conn.Close()
    }
    t.connections = nil
    t.mu.Unlock()

    // 3. ポートを解放
    t.allocator.Release(t.port)

    slog.Info("tunnel closed", "tunnel_id", t.ID, "port", t.port)
    return nil
}
```

```go
// ❌ Anti-Pattern: リソース解放漏れ
func (t *Tunnel) Close() error {
    t.listener.Close()
    // connections が残ったまま → goroutine リーク
    // ポートが解放されない → ポート枯渇
    return nil
}
```

#### データコネクションのマッチング

外部接続とクライアントのデータコネクションを安全にマッチングするパターンです。

```go
// ✅ Best Practice: タイムアウト付きチャネルでマッチング
type pendingConn struct {
    ch      chan net.Conn
    created time.Time
}

func (s *Server) waitForDataConn(connID string, timeout time.Duration) (net.Conn, error) {
    pc := &pendingConn{
        ch:      make(chan net.Conn, 1),
        created: time.Now(),
    }
    s.pending.Store(connID, pc)
    defer s.pending.Delete(connID)

    select {
    case conn := <-pc.ch:
        return conn, nil
    case <-time.After(timeout):
        return nil, fmt.Errorf("data connection timeout for %s: %w", connID, ErrTimeout)
    }
}
```

```go
// ❌ Anti-Pattern: ポーリングで待機
func (s *Server) waitForDataConn(connID string) net.Conn {
    for {
        if conn, ok := s.dataConns[connID]; ok { // data race
            return conn
        }
        time.Sleep(100 * time.Millisecond) // CPU 浪費 + 無限ループの危険
    }
}
```

#### Graceful Shutdown

```go
// ✅ Best Practice: シグナルハンドリング + context キャンセル
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer cancel()

    srv := server.NewServer(cfg, logger)
    if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        slog.Error("server stopped with error", "err", err)
        os.Exit(1)
    }
    slog.Info("server stopped gracefully")
}
```

```go
// ❌ Anti-Pattern: os.Exit でぶった切る
func main() {
    srv := server.NewServer(cfg, logger)
    go srv.Run(context.Background())

    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt)
    <-c
    os.Exit(0) // クリーンアップなし。接続が残留
}
```

#### 再接続戦略 (クライアント側)

```go
// ✅ Best Practice: Exponential Backoff + Jitter
func (c *Client) connectWithRetry(ctx context.Context) (net.Conn, error) {
    backoff := 1 * time.Second
    maxBackoff := 30 * time.Second

    for {
        conn, err := net.DialTimeout("tcp", c.cfg.ServerAddr, 10*time.Second)
        if err == nil {
            slog.Info("connected to server", "addr", c.cfg.ServerAddr)
            return conn, nil
        }
        slog.Warn("connection failed, retrying", "err", err, "backoff", backoff)

        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-time.After(backoff + time.Duration(rand.Int63n(int64(backoff/2)))): // jitter
        }

        backoff *= 2
        if backoff > maxBackoff {
            backoff = maxBackoff
        }
    }
}
```

```go
// ❌ Anti-Pattern: 固定間隔 Sleep リトライ
func (c *Client) connectWithRetry() net.Conn {
    for {
        conn, err := net.Dial("tcp", c.cfg.ServerAddr) // タイムアウトなし
        if err == nil {
            return conn
        }
        time.Sleep(5 * time.Second) // 固定間隔、キャンセル不能、context 無視
    }
}
```

#### ID 生成

```go
// ✅ Best Practice: crypto/rand ベースの短い一意 ID
import "crypto/rand"

func generateID() string {
    b := make([]byte, 8)
    if _, err := rand.Read(b); err != nil {
        panic("crypto/rand failed: " + err.Error()) // ここだけは panic 許容（OS レベル障害）
    }
    return hex.EncodeToString(b)
}
```

```go
// ❌ Anti-Pattern: math/rand で予測可能な ID
import "math/rand"

func generateID() string {
    return fmt.Sprintf("%d", rand.Int()) // 予測可能、衝突しやすい
}
```

#### Read/Write デッドラインの設定

```go
// ✅ Best Practice: すべてのネットワーク I/O にデッドラインを設ける
func (s *Server) readControlMessage(conn net.Conn) (byte, []byte, error) {
    conn.SetReadDeadline(time.Now().Add(60 * time.Second))
    defer conn.SetReadDeadline(time.Time{}) // リセット

    return protocol.ReadFrame(conn)
}
```

```go
// ❌ Anti-Pattern: デッドラインなしで Read
func (s *Server) readControlMessage(conn net.Conn) (byte, []byte, error) {
    return protocol.ReadFrame(conn) // 相手が何も送らないと永久にブロック
}
```

#### バッファサイズとメモリ安全性

```go
// ✅ Best Practice: ペイロード長の上限チェック
const maxPayloadSize = 1 << 20 // 1MB

func ReadFrame(conn net.Conn) (byte, []byte, error) {
    var header [5]byte
    if _, err := io.ReadFull(conn, header[:]); err != nil {
        return 0, nil, fmt.Errorf("read frame header: %w", err)
    }
    msgType := header[0]
    length := binary.BigEndian.Uint32(header[1:5])

    if length > maxPayloadSize {
        return 0, nil, fmt.Errorf("payload too large (%d bytes): %w", length, ErrInvalidMessage)
    }

    payload := make([]byte, length)
    if _, err := io.ReadFull(conn, payload); err != nil {
        return 0, nil, fmt.Errorf("read frame payload: %w", err)
    }
    return msgType, payload, nil
}
```

```go
// ❌ Anti-Pattern: 長さを検証せずにメモリ確保
func ReadFrame(conn net.Conn) (byte, []byte, error) {
    // ...
    length := binary.BigEndian.Uint32(header[1:5])
    payload := make([]byte, length) // 攻撃者が 0xFFFFFFFF を送ると 4GB 確保しようとする
    // ...
}
```

---

### アンチパターン一覧

| #   | アンチパターン                           | 問題                                 | 正しいアプローチ                                   |
| --- | ---------------------------------------- | ------------------------------------ | -------------------------------------------------- |
| 1   | `go func() { ... }()` にコメントなし     | goroutine の目的が不明、リークの温床 | `// goroutine: <目的>` を必ず付ける                |
| 2   | `conn.Close()` を呼び忘れる              | fd リーク → "too many open files"    | `defer conn.Close()` を Accept/Dial 直後に書く     |
| 3   | mutex を `defer` なしで使う              | panic 時にデッドロック               | `defer mu.Unlock()` を `Lock()` の直後に書く       |
| 4   | チャネルを閉じ忘れる                     | 受信側が永久にブロック               | 送信側が責任を持って `close()` する                |
| 5   | `select {}` でブロック                   | CPU を食わないが意図が不明           | `<-ctx.Done()` で待機する                          |
| 6   | エラーを `_` で握りつぶす                | 障害原因の追跡不能                   | 少なくとも `slog.Debug` で記録する                 |
| 7   | 1 パッケージに全部入れる                 | 変更影響が全体に波及                 | 責務でパッケージ分割                               |
| 8   | `sync.Map` を安易に使う                  | 型安全性がない                       | 普通の `map` + `sync.RWMutex`                      |
| 9   | テストで実ポートを使う                   | CI でポート衝突                      | `net.Pipe()` でインメモリテスト                    |
| 10  | `init()` で副作用を起こす                | テスト順序依存、DI 不能              | コンストラクタで明示的に初期化                     |
| 11  | context を構造体に保存する               | ライフサイクルが不明確になる         | メソッド引数として毎回渡す                         |
| 12  | 巨大な `switch` でメッセージ処理         | 新メッセージ追加時に肥大化           | ハンドラーマップ `map[byte]HandlerFunc`            |
| 13  | `string` で ID を比較 (大文字小文字混在) | 比較ミスによるバグ                   | `strings.EqualFold` or 一貫した生成ルール          |
| 14  | 戻り値の error を無名で返す              | スタックトレースがない               | `fmt.Errorf("関数名: %w", err)` でコンテキスト付与 |

---

## ディレクトリ構成ルール

```
cmd/soralink-server/main.go   # エントリポイントのみ。ロジックは書かない
cmd/soralink-client/main.go   # 同上
internal/protocol/            # 通信プロトコル（server/client 両方から使う共通層）
internal/server/              # サーバー固有ロジック
internal/client/              # クライアント固有ロジック
configs/                      # YAML 設定ファイルサンプル
deploy/                       # systemd / Dockerfile 等のデプロイ成果物
doc/                          # 設計ドキュメント（コード生成不要）
```

- `cmd/` のファイルは薄いラッパーにする。`main()` で設定を読んで `server.New(cfg).Run(ctx)` のように委譲する
- `internal/` 以下のパッケージはモジュール外から import できない設計を維持する
- `internal/server` と `internal/client` は互いに import しない

---

## 命名規則

### パッケージ名

| 場所                | パッケージ名 | 例                         |
| ------------------- | ------------ | -------------------------- |
| `internal/protocol` | `protocol`   | `protocol.WriteFrame(...)` |
| `internal/server`   | `server`     | `server.New(cfg)`          |
| `internal/client`   | `client`     | `client.New(cfg)`          |

- パッケージ名は **小文字・単数形**。`servers`, `utils`, `helpers` などは使わない
- ファイル名はパッケージ内の責務単位で付ける: `tunnel.go`, `bridge.go`, `frame.go`

### 型・変数

```go
// ✅ Good
type TunnelManager struct { ... }
type ServerConfig struct { ... }
var controlPort = 4610

// ❌ Bad
type tunnel_manager struct { ... }
type SrvCfg struct { ... }       // 省略しすぎ
var ControlPort = 4610           // 不必要なエクスポート
```

| 種別                       | 規則                            | 例                                  |
| -------------------------- | ------------------------------- | ----------------------------------- |
| エクスポートされる型       | `PascalCase`                    | `TunnelManager`, `ServerConfig`     |
| エクスポートされない型     | `camelCase`                     | `tunnelEntry`, `bridgePair`         |
| インターフェース           | 動詞 + `er` suffix              | `Tunneler`, `Bridger`               |
| 定数（メッセージタイプ等） | `MsgType` prefix + `PascalCase` | `MsgTypeAuth`, `MsgTypePing`        |
| エラー変数                 | `Err` prefix                    | `ErrAuthFailed`, `ErrPortExhausted` |

### 関数・メソッド

```go
// コンストラクタは New + 型名、または New だけ
func NewServer(cfg *ServerConfig) *Server { ... }
func New(cfg *ClientConfig) *Client { ... }

// 実行・開始は Run
func (s *Server) Run(ctx context.Context) error { ... }

// クリーンアップは Close
func (t *Tunnel) Close() error { ... }
```

---

## DTO（Data Transfer Object）設計

プロトコル層の **メッセージ構造体** が DTO に相当します。

### 設計方針

1. **`internal/protocol/message.go` に集約する** — すべての送受信メッセージ構造体はここで定義
2. **JSON タグを必ず付ける** — フィールド名は `snake_case`
3. **バリデーションはメソッドとして持つ** — 構造体に `Validate() error` を実装
4. **DTO は不変設計** — 一度作ったらフィールドを直接書き換えない。必要なら新しいインスタンスを生成

```go
// ✅ Good: internal/protocol/message.go
package protocol

type MsgAuth struct {
    Token string `json:"token"`
}

func (m *MsgAuth) Validate() error {
    if m.Token == "" {
        return ErrEmptyToken
    }
    return nil
}

type MsgRequestTunnel struct {
    LocalPort     int    `json:"local_port"`
    Protocol      string `json:"protocol"`       // "tcp"
    RequestedPort int    `json:"requested_port"` // 0 = 自動割り当て
}

type MsgTunnelReady struct {
    TunnelID   string `json:"tunnel_id"`
    RemotePort int    `json:"remote_port"`
    URL        string `json:"url"`
}

type MsgNewConnection struct {
    TunnelID     string `json:"tunnel_id"`
    ConnectionID string `json:"connection_id"`
}
```

```go
// ❌ Bad: サーバーロジック内にメッセージ構造体を散らばらせる
package server

type authMsg struct { Token string } // protocol に置くべき
```

### メッセージタイプ定数

```go
// internal/protocol/message.go 内で定義
const (
    MsgTypeAuth           byte = 0x01
    MsgTypeAuthResp       byte = 0x02
    MsgTypeRequestTunnel  byte = 0x03
    MsgTypeTunnelReady    byte = 0x04
    MsgTypeNewConnection  byte = 0x05
    MsgTypePing           byte = 0x06
    MsgTypePong           byte = 0x07
    MsgTypeError          byte = 0x08
    MsgTypeCloseTunnel    byte = 0x09
)
```

---

## DI（依存性注入）

**コンストラクタ注入** を使います。グローバル変数・`init()` への依存は禁止。

### 基本パターン

```go
// ✅ Good: コンストラクタで依存を受け取る
type Server struct {
    cfg     *ServerConfig
    tunnels *TunnelManager
    logger  *slog.Logger
}

func NewServer(cfg *ServerConfig, logger *slog.Logger) *Server {
    return &Server{
        cfg:     cfg,
        tunnels: NewTunnelManager(cfg.PortRange),
        logger:  logger,
    }
}
```

```go
// ❌ Bad: グローバル状態に依存
var globalLogger *slog.Logger // 禁止

func NewServer(cfg *ServerConfig) *Server {
    return &Server{cfg: cfg, logger: globalLogger} // テスト不能
}
```

### インターフェースによる抽象化（テスト容易性のため）

将来的に差し替えが想定されるコンポーネントはインターフェースを定義する：

```go
// internal/server/tunnel.go
type PortAllocator interface {
    Allocate() (int, error)
    Release(port int)
}

// 本番実装
type rangePortAllocator struct {
    mu   sync.Mutex
    used map[int]bool
    min  int
    max  int
}

// Server はインターフェースに依存
type Server struct {
    alloc PortAllocator
}
```

### `context.Context` の伝播

- すべての `Run()`, `Accept()`, `Serve()` 相当のメソッドは `ctx context.Context` を第一引数に取る
- `ctx.Done()` のキャンセルを尊重し、goroutine を適切に終了させる

```go
func (s *Server) Run(ctx context.Context) error {
    go func() {
        <-ctx.Done()
        s.listener.Close() // Accept をブロックから抜ける
    }()
    for {
        conn, err := s.listener.Accept()
        if err != nil {
            return ctx.Err() // キャンセル起因のエラーは包んで返す
        }
        go s.handleClient(ctx, conn)
    }
}
```

---

## エラーハンドリング

### 宣言済みエラー変数

```go
// internal/protocol/errors.go
var (
    ErrAuthFailed      = errors.New("authentication failed")
    ErrPortExhausted   = errors.New("no available port in range")
    ErrTunnelNotFound  = errors.New("tunnel not found")
    ErrInvalidMessage  = errors.New("invalid message format")
)
```

### ラッピング

```go
// ✅ %w でエラーをラップ（errors.Is / errors.As が使える）
if err := frame.Write(conn, msg); err != nil {
    return fmt.Errorf("send TunnelReady to %s: %w", conn.RemoteAddr(), err)
}

// ❌ fmt.Errorf で %v を使うとラップされない
return fmt.Errorf("send failed: %v", err)
```

### ネットワークエラーの扱い

```go
// 接続切断は expected error — panic しない、ログも DEBUG レベルで十分
if err := bridge(conn1, conn2); err != nil {
    if !isClosedConnError(err) {
        slog.Warn("bridge error", "err", err)
    }
}

func isClosedConnError(err error) bool {
    return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}
```

---

## 並行処理

### goroutine 起動のルール

1. **goroutine を起動したら必ず終了を保証する** — `WaitGroup` か `errgroup` を使う
2. **goroutine 内でパニックを握りつぶさない** — `recover` して `slog.Error` でログを出す
3. **goroutine には名前をつける** (コメントで明示)

```go
// ✅ Good
go func() { // goroutine: handle external connection
    defer func() {
        if r := recover(); r != nil {
            slog.Error("panic in connection handler", "recover", r)
        }
    }()
    s.handleConn(ctx, conn)
}()
```

### 共有リソースの保護

```go
type TunnelManager struct {
    mu      sync.RWMutex
    tunnels map[string]*Tunnel
}

func (m *TunnelManager) Get(id string) (*Tunnel, bool) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    t, ok := m.tunnels[id]
    return t, ok
}

func (m *TunnelManager) Add(t *Tunnel) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.tunnels[t.ID] = t
}
```

### ブリッジの実装パターン

```go
// internal/server/bridge.go
// Bridge は conn1 と conn2 を双方向にコピーする。
// どちらか一方が閉じたらもう一方も閉じる。
func Bridge(conn1, conn2 net.Conn) {
    var wg sync.WaitGroup
    wg.Add(2)
    copy := func(dst, src net.Conn) {
        defer wg.Done()
        defer dst.Close()
        io.Copy(dst, src) //nolint:errcheck // EOF は正常
    }
    go copy(conn1, conn2)
    go copy(conn2, conn1)
    wg.Wait()
}
```

---

## ログ

`log/slog`（Go 1.21 標準）を使用。独自ロガーライブラリは導入しない。

```go
// ✅ Good: 構造化ログ + 適切なレベル
slog.Info("tunnel created", "tunnel_id", t.ID, "remote_port", t.RemotePort)
slog.Debug("new connection", "connection_id", connID, "remote_addr", conn.RemoteAddr())
slog.Warn("client disconnected unexpectedly", "err", err)
slog.Error("failed to allocate port", "err", err, "range_min", min, "range_max", max)

// ❌ Bad: フォーマット文字列でコンテキストを埋める
log.Printf("tunnel %s created on port %d", t.ID, t.RemotePort)
```

---

## テスト

### ファイル配置

- テストファイルは実装と同じパッケージに置く: `frame_test.go` は `package protocol`
- 統合テストは `_test.go` suffix でパッケージ外: `package protocol_test`

### テストパターン

```go
// テーブル駆動テスト
func TestWriteReadFrame(t *testing.T) {
    tests := []struct {
        name    string
        msgType byte
        payload []byte
    }{
        {"auth message", MsgTypeAuth, []byte(`{"token":"abc"}`)},
        {"empty payload", MsgTypePing, []byte{}},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // net.Pipe() でインメモリ接続を使いテストする
            conn1, conn2 := net.Pipe()
            defer conn1.Close()
            defer conn2.Close()
            // ...
        })
    }
}
```

- ネットワーク部分のテストは `net.Pipe()` を使い、実際のポートを使わない
- 外部依存（ポート割り当て等）はインターフェースでモック可能にする

---

## Git 規約

### ブランチ命名規則

| プレフィックス | 用途                         | 例                                |
| -------------- | ---------------------------- | --------------------------------- |
| `feature/`     | 新機能の追加                 | `feature/ping-pong-healthcheck`   |
| `docs/`        | ドキュメントのみの変更       | `docs/add-git-conventions`        |
| `fix/`         | バグ修正                     | `fix/tunnel-port-leak`            |
| `hotfix/`      | 本番障害の緊急修正           | `hotfix/auth-token-panic`         |
| `refactor/`    | 機能変更なしのコード改善     | `refactor/tunnel-manager-cleanup` |
| `test/`        | テストの追加・修正のみ       | `test/frame-roundtrip-edge-cases` |
| `chore/`       | ビルド・CI・依存関係等の雑務 | `chore/update-go-version`         |

**ルール:**

- ブランチ名は **小文字 + ハイフン区切り**。スペース・スラッシュ 2 個以上は使わない
- `main` ブランチへの直接コミットは禁止。必ずブランチを切る
- 作業完了後は Pull Request を作成し、レビュー後にマージする

### コミットメッセージ規約

コミットメッセージは **日本語** で書く。フォーマットは以下の通り:

```
<type>: <件名（日本語・命令形）>

[本文（任意）: 変更の背景・理由を説明]

[フッター（任意）: 関連 Issue など]
```

**type 一覧:**

| type       | 用途                                     |
| ---------- | ---------------------------------------- |
| `feat`     | 新機能の追加                             |
| `fix`      | バグ修正                                 |
| `docs`     | ドキュメントのみの変更                   |
| `refactor` | 機能変更なしのリファクタリング           |
| `test`     | テストの追加・修正                       |
| `chore`    | ビルド設定・依存関係・CI の変更          |
| `perf`     | パフォーマンス改善                       |
| `style`    | フォーマット・スペース等（動作変更なし） |

**コミットメッセージの例:**

```
# ✅ Good
feat: Ping/Pong ヘルスチェック機能を追加

半開き接続を 60 秒以内に検出して切断する。
サーバーが 30 秒ごとに Ping を送り、Pong が返らなければ接続を閉じる。

fix: トンネル削除時のポートリーク問題を修正

Close() 呼び出し後もポートが解放されないバグを修正。
TunnelManager のロック順序を見直した。

docs: MVP 実装手順書を追加

refactor: TunnelManager の排他制御を sync.RWMutex に変更
```

```
# ❌ Bad
updated                         # 何を更新した？
fix bug                         # 英語・内容不明
feat: Add ping pong healthcheck # 英語
WIP                             # コミット単位が大きすぎる可能性
```

**ルール:**

- 件名は **50 文字以内** を目安にする
- 本文は件名と **空行で区切る**
- 本文には「なぜ変更したか」を書く（「何を変更したか」はコードを見ればわかる）
- `WIP` コミットは `main` にマージしない（マージ前に `git rebase -i` で整理）

---

## 禁止事項

| 禁止                                             | 理由                           | 代替                               |
| ------------------------------------------------ | ------------------------------ | ---------------------------------- |
| `panic()` をエラー制御に使う                     | goroutine 全体がクラッシュする | `error` を返す                     |
| `log.Fatal()` / `os.Exit()` を `main()` 外で使う | テスト・終了処理を妨げる       | `error` で伝播、`main()` で終了    |
| グローバル変数でロガー・設定を持つ               | テスト干渉・DI 破壊            | コンストラクタ注入                 |
| `time.Sleep()` をリトライに使う                  | CPU 無駄遣い                   | `time.After` + exponential backoff |
| `interface{}` / `any` を多用する                 | 型安全性の喪失                 | 具体的な型か型パラメータを使う     |
| コメントなしの goroutine 起動                    | 意図が不明                     | 何をする goroutine か 1 行コメント |

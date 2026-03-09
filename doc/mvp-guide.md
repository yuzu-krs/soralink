# Soralink MVP 実装手順書

> **ゴール:** ローカルの TCP ポートを、グローバル IP 付き VPS 経由でインターネットに公開する最小構成を動かす

---

## 前提条件

| 項目             | 要件                                           |
| ---------------- | ---------------------------------------------- |
| Go               | 1.22 以上                                      |
| VPS              | グローバル IP が割り当て済み、SSH アクセス可能 |
| OS (VPS)         | Linux (Ubuntu 22.04+ 推奨)                     |
| OS (ローカル)    | Windows / macOS / Linux いずれか               |
| ファイアウォール | VPS 上でポート 4610, 10000-20000 を開放可能    |

---

## Phase 0: プロジェクトの初期化

### 0-1. Go モジュール作成

```bash
cd soralink
go mod init github.com/<yourname>/soralink
```

### 0-2. ディレクトリ構成の作成

```bash
mkdir -p cmd/soralink-server cmd/soralink-client
mkdir -p internal/protocol internal/server internal/client
mkdir -p configs deploy
```

最終的なツリー:

```
soralink/
├── cmd/
│   ├── soralink-server/main.go
│   └── soralink-client/main.go
├── internal/
│   ├── protocol/
│   │   ├── frame.go
│   │   ├── frame_test.go
│   │   ├── message.go
│   │   └── errors.go
│   ├── server/
│   │   ├── server.go
│   │   ├── tunnel.go
│   │   ├── bridge.go
│   │   └── config.go
│   └── client/
│       ├── client.go
│       ├── proxy.go
│       └── config.go
├── configs/
│   ├── server.yaml
│   └── client.yaml
├── deploy/
│   └── soralink.service
├── go.mod
└── Makefile
```

### 0-3. Makefile を作成

```makefile
.PHONY: build-server build-client build test clean

build: build-server build-client

build-server:
	go build -o bin/soralink-server ./cmd/soralink-server

build-client:
	go build -o bin/soralink-client ./cmd/soralink-client

build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/soralink-server-linux ./cmd/soralink-server

test:
	go test -v -race ./...

clean:
	rm -rf bin/
```

---

## Phase 1: プロトコル層 (`internal/protocol/`)

> すべての通信の基盤。server/client 両方が依存する共通パッケージ。

### 1-1. エラー定義 — `internal/protocol/errors.go`

定義するエラー:

```go
var (
    ErrAuthFailed     = errors.New("authentication failed")
    ErrPortExhausted  = errors.New("no available port in range")
    ErrTunnelNotFound = errors.New("tunnel not found")
    ErrInvalidMessage = errors.New("invalid message format")
    ErrEmptyToken     = errors.New("auth token is empty")
    ErrPayloadTooLarge = errors.New("payload exceeds maximum size")
)
```

**チェックリスト:**

- [ ] `errors.New` で宣言（`fmt.Errorf` ではない）
- [ ] 消費側で `errors.Is(err, protocol.ErrAuthFailed)` が使えることを確認

### 1-2. メッセージ型定義 — `internal/protocol/message.go`

**メッセージタイプ定数:**

```go
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
    MsgTypeDataConnInit  byte = 0x0A  // データコネクション初期化
)
```

**メッセージ構造体:**

| 構造体             | フィールド                                 | 方向            |
| ------------------ | ------------------------------------------ | --------------- |
| `MsgAuth`          | `token`                                    | Client → Server |
| `MsgAuthResp`      | `success`, `message`                       | Server → Client |
| `MsgRequestTunnel` | `local_port`, `protocol`, `requested_port` | Client → Server |
| `MsgTunnelReady`   | `tunnel_id`, `remote_port`, `url`          | Server → Client |
| `MsgNewConnection` | `tunnel_id`, `connection_id`               | Server → Client |
| `MsgDataConnInit`  | `connection_id`                            | Client → Server |
| `MsgError`         | `message`                                  | 双方向          |

**実装手順:**

1. 上記の構造体すべてに `json:"snake_case"` タグを付ける
2. `MsgAuth` に `Validate() error` を実装（空トークンチェック）
3. JSON シリアライズ用ヘルパー:
   - `MarshalMessage(msgType byte, v any) ([]byte, error)` — 構造体 → JSON bytes
   - `UnmarshalMessage(msgType byte, data []byte) (any, error)` — JSON bytes → 構造体

### 1-3. フレーム読み書き — `internal/protocol/frame.go`

**フレーム構造:**

```
+----------+----------+------------------+
| Type (1B)| Len (4B) | Payload (可変)    |
+----------+----------+------------------+
```

**実装する関数:**

```go
const MaxPayloadSize = 1 << 20 // 1MB

// WriteFrame はフレームを conn に書き込む
func WriteFrame(conn net.Conn, msgType byte, payload []byte) error

// ReadFrame はフレームを conn から読み取る
func ReadFrame(conn net.Conn) (msgType byte, payload []byte, err error)

// WriteMessage は構造体を JSON エンコードしてフレームとして送信する
func WriteMessage(conn net.Conn, msgType byte, v any) error

// ReadMessage はフレームを読み取って JSON デコードする
func ReadMessage(conn net.Conn) (msgType byte, payload []byte, err error)
```

**実装時の注意:**

- `io.ReadFull` を使って確実にヘッダー 5 バイトを読む
- ペイロードサイズが `MaxPayloadSize` を超えたらエラー
- `binary.BigEndian` でバイトオーダーを統一

### 1-4. フレームのテスト — `internal/protocol/frame_test.go`

**テストケースリスト:**

| テスト名                          | 内容                         |
| --------------------------------- | ---------------------------- |
| `TestWriteReadFrame_RoundTrip`    | 書いたものが読めることを確認 |
| `TestWriteReadFrame_EmptyPayload` | ペイロード 0 バイトでも動作  |
| `TestWriteReadFrame_LargePayload` | 上限ギリギリのペイロード     |
| `TestReadFrame_PayloadTooLarge`   | 上限超過でエラー             |
| `TestWriteReadMessage_Auth`       | MsgAuth の往復テスト         |

すべて `net.Pipe()` でテスト。実ポートは使わない。

```go
func TestWriteReadFrame_RoundTrip(t *testing.T) {
    conn1, conn2 := net.Pipe()
    defer conn1.Close()
    defer conn2.Close()

    go func() {
        WriteFrame(conn1, MsgTypeAuth, []byte(`{"token":"test"}`))
    }()

    msgType, payload, err := ReadFrame(conn2)
    // assert msgType == MsgTypeAuth
    // assert payload matches
    // assert err == nil
}
```

**完了条件:** `go test -v -race ./internal/protocol/` が ALL PASS

---

## Phase 2: サーバー実装 (`internal/server/`)

### 2-1. 設定構造体 — `internal/server/config.go`

```go
type Config struct {
    ControlPort int    `yaml:"control_port"` // デフォルト 4610
    AuthToken   string `yaml:"auth_token"`
    PortRange   struct {
        Min int `yaml:"min"` // デフォルト 10000
        Max int `yaml:"max"` // デフォルト 20000
    } `yaml:"port_range"`
    LogLevel string `yaml:"log_level"` // "debug", "info", "warn", "error"
}
```

**実装手順:**

1. `Config` 定義
2. `Validate() error` — `AuthToken` が空なら即エラー、ポート範囲の妥当性チェック
3. `LoadConfig(path string) (*Config, error)` — YAML ファイル読み込み
4. `EffectivePortRange() (int, int)` — ゼロ値ならデフォルトを返すメソッド

### 2-2. メインサーバー — `internal/server/server.go`

**Server 構造体:**

```go
type Server struct {
    cfg      *Config
    logger   *slog.Logger
    listener net.Listener
    tunnels  *TunnelManager
    pending  sync.Map // map[connectionID]*pendingConn
}
```

**実装する関数/メソッド:**

| 関数                                                                       | 責務                        |
| -------------------------------------------------------------------------- | --------------------------- |
| `NewServer(cfg *Config, logger *slog.Logger) *Server`                      | コンストラクタ              |
| `(s *Server) Run(ctx context.Context) error`                               | Listen して acceptLoop 開始 |
| `(s *Server) acceptLoop(ctx context.Context) error`                        | Accept → 振り分け           |
| `(s *Server) handleControlConn(ctx context.Context, conn net.Conn)`        | 制御コネクション処理        |
| `(s *Server) handleDataConn(conn net.Conn, msg *protocol.MsgDataConnInit)` | データコネクション処理      |
| `(s *Server) authenticate(conn net.Conn) error`                            | トークン照合                |

**処理フロー:**

```
Run(ctx)
  └── Listen(:4610)
      └── acceptLoop(ctx)
            └── Accept
                 ├── ReadFrame (最初のフレームで制御/データを判別)
                 │
                 ├── [MsgTypeAuth] → handleControlConn
                 │     ├── authenticate (トークン照合)
                 │     ├── AuthResp 送信
                 │     └── メッセージループ:
                 │           ├── [MsgTypeRequestTunnel] → tunnel 作成
                 │           ├── [MsgTypePing] → Pong 返送
                 │           └── [MsgTypeCloseTunnel] → tunnel 破棄
                 │
                 └── [MsgTypeDataConnInit] → handleDataConn
                       └── connectionID でペンディング接続とマッチング
```

**実装の注意:**

- `Accept` 後、最初のフレームが `MsgTypeAuth` なら制御コネクション、`MsgTypeDataConnInit` ならデータコネクション
- 制御コネクションの handleControlConn はクライアント切断まで長生きする goroutine
- 制御コネクション切断時は、そのクライアントの全トンネルをクリーンアップ

### 2-3. トンネル管理 — `internal/server/tunnel.go`

**TunnelManager 構造体:**

```go
type TunnelManager struct {
    mu      sync.RWMutex
    tunnels map[string]*Tunnel
    ports   map[int]bool // 使用中ポート
    minPort int
    maxPort int
}

type Tunnel struct {
    ID          string
    RemotePort  int
    ClientConn  net.Conn     // 制御コネクション (NewConnection 通知用)
    listener    net.Listener // パブリックポートの Listener
    connections sync.Map     // アクティブなデータコネクション
}
```

**実装する関数/メソッド:**

| 関数                                                                                 | 責務                                       |
| ------------------------------------------------------------------------------------ | ------------------------------------------ |
| `NewTunnelManager(min, max int) *TunnelManager`                                      | コンストラクタ                             |
| `(m *TunnelManager) Create(clientConn net.Conn, requestedPort int) (*Tunnel, error)` | トンネル作成 + ポート割り当て              |
| `(m *TunnelManager) Get(id string) (*Tunnel, bool)`                                  | ID でトンネル取得                          |
| `(m *TunnelManager) Remove(id string)`                                               | トンネル破棄 + リソース解放                |
| `(m *TunnelManager) RemoveByClient(conn net.Conn)`                                   | クライアント切断時の一括削除               |
| `(t *Tunnel) AcceptLoop(ctx context.Context, server *Server)`                        | 外部接続の受付ループ                       |
| `(t *Tunnel) Close() error`                                                          | リスナー停止 + 全接続クローズ + ポート解放 |

**トンネル作成フロー:**

```
Create(clientConn, requestedPort)
  ├── requestedPort > 0 ? そのポートを確保 : 空きポートを探す
  ├── net.Listen("tcp", ":port")
  ├── Tunnel 構造体を作成
  ├── tunnels マップに登録
  └── go tunnel.AcceptLoop(ctx, server)  // goroutine: accept external connections
```

**外部接続の受付フロー (AcceptLoop):**

```
AcceptLoop(ctx, server)
  └── for {
        conn := listener.Accept()
        connID := generateID()
        ├── pending に登録 (chan net.Conn で待機用)
        ├── protocol.WriteMessage(ClientConn, MsgTypeNewConnection, {tunnel_id, connection_id})
        └── go func() {  // goroutine: wait for data connection match
              dataConn := <-pending[connID].ch  (タイムアウト 10 秒)
              Bridge(conn, dataConn)
            }()
      }
```

### 2-4. ブリッジ — `internal/server/bridge.go`

```go
// Bridge は 2 つの net.Conn を双方向にコピーする。
// どちらか一方が閉じたらもう一方も閉じる。
func Bridge(conn1, conn2 net.Conn) {
    var wg sync.WaitGroup
    wg.Add(2)

    copy := func(dst, src net.Conn) { // goroutine: bridge one direction
        defer wg.Done()
        defer dst.Close()
        io.Copy(dst, src)
    }

    go copy(conn1, conn2) // goroutine: bridge direction 1→2
    go copy(conn2, conn1) // goroutine: bridge direction 2→1
    wg.Wait()
}
```

**テスト:** `net.Pipe()` 2 組を使って双方向データ転送を検証

---

## Phase 3: クライアント実装 (`internal/client/`)

### 3-1. 設定構造体 — `internal/client/config.go`

```go
type Config struct {
    ServerAddr string `yaml:"server"`
    AuthToken  string `yaml:"auth_token"`
    Tunnels    []TunnelConfig `yaml:"tunnels"`
    LogLevel   string `yaml:"log_level"`
}

type TunnelConfig struct {
    LocalPort     int    `yaml:"local_port"`
    RemotePort    int    `yaml:"remote_port"`    // 0 = 自動割り当て
    Protocol      string `yaml:"protocol"`       // "tcp"
}
```

**実装手順:**

1. 構造体定義
2. `Validate() error` — ServerAddr 必須、Tunnels 1 件以上、LocalPort > 0
3. `LoadConfig(path string) (*Config, error)`

### 3-2. メインクライアント — `internal/client/client.go`

**Client 構造体:**

```go
type Client struct {
    cfg      *Config
    logger   *slog.Logger
    ctrlConn net.Conn // 制御コネクション
}
```

**実装する関数/メソッド:**

| 関数                                                  | 責務                                          |
| ----------------------------------------------------- | --------------------------------------------- |
| `NewClient(cfg *Config, logger *slog.Logger) *Client` | コンストラクタ                                |
| `(c *Client) Run(ctx context.Context) error`          | 接続 → 認証 → トンネル要求 → メッセージループ |
| `(c *Client) connect(ctx context.Context) error`      | サーバーへ TCP 接続                           |
| `(c *Client) authenticate() error`                    | Auth 送信 → AuthResp 受信                     |
| `(c *Client) requestTunnels() error`                  | 各 TunnelConfig に対して RequestTunnel 送信   |
| `(c *Client) messageLoop(ctx context.Context) error`  | 制御メッセージの受信ループ                    |

**処理フロー:**

```
Run(ctx)
  ├── connect(ctx)  — サーバーに TCP 接続
  ├── authenticate()  — Auth → AuthResp
  ├── requestTunnels()  — RequestTunnel × N → TunnelReady × N
  │     └── 表示: "✓ Tunnel: <VPS_IP>:<port> → localhost:<local_port>"
  └── messageLoop(ctx)
        └── for {
              ReadFrame()
              switch msgType:
                [MsgTypeNewConnection] → go handleNewConnection(msg)
                [MsgTypePing]          → WritePong()
                [MsgTypeError]         → slog.Error(msg)
            }
```

### 3-3. プロキシ処理 — `internal/client/proxy.go`

**実装する関数:**

```go
func (c *Client) handleNewConnection(ctx context.Context, msg *protocol.MsgNewConnection)
```

**処理フロー:**

```
handleNewConnection(ctx, msg)
  ├── net.Dial("tcp", serverAddr)           // サーバーにデータコネクション確立
  ├── WriteMessage(MsgTypeDataConnInit, {connection_id})  // ID を送信
  ├── net.Dial("tcp", "localhost:<localPort>")  // ローカルサービスに接続
  └── Bridge(serverConn, localConn)         // 双方向ブリッジ
```

**エラーケースの処理:**

- サーバーへのデータコネクション確立失敗 → `slog.Error` + return
- ローカルサービスへの接続失敗 → サーバー接続を閉じて return
- ブリッジ中の切断 → 両方閉じて return（正常終了）

---

## Phase 4: CLI エントリポイント (`cmd/`)

### 4-1. サーバー — `cmd/soralink-server/main.go`

**処理内容:**

1. CLI フラグ解析 (`--config`)
2. YAML 設定ファイル読み込み
3. バリデーション
4. slog ロガー初期化（設定の `log_level` に従う）
5. シグナルハンドリング (`signal.NotifyContext`)
6. `server.NewServer(cfg, logger).Run(ctx)`
7. エラーハンドリング + 終了コード

```go
func main() {
    configPath := flag.String("config", "configs/server.yaml", "config file path")
    flag.Parse()

    cfg, err := server.LoadConfig(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "config error: %v\n", err)
        os.Exit(1)
    }
    if err := cfg.Validate(); err != nil {
        fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
        os.Exit(1)
    }

    logger := initLogger(cfg.LogLevel)
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer cancel()

    srv := server.NewServer(cfg, logger)
    if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        logger.Error("server stopped with error", "err", err)
        os.Exit(1)
    }
    logger.Info("server stopped gracefully")
}
```

### 4-2. クライアント — `cmd/soralink-client/main.go`

**CLI フラグ (config ファイルとの併用):**

| フラグ     | 説明                            | デフォルト            |
| ---------- | ------------------------------- | --------------------- |
| `--config` | 設定ファイルパス                | `configs/client.yaml` |
| `--server` | サーバーアドレス (上書き用)     | -                     |
| `--token`  | 認証トークン (上書き用)         | -                     |
| `--local`  | ローカルポート (簡易モード)     | -                     |
| `--remote` | リモートポート希望 (簡易モード) | 0                     |

**2 つの使い方:**

```bash
# 設定ファイルモード
soralink-client --config client.yaml

# クイックモード (設定ファイル不要)
soralink-client --server your-vps:4610 --token abc123 --local 3000
```

**実装:** クイックモードのフラグが指定されていたら `Config` を直接構築、なければ YAML を読む

---

## Phase 5: 設定ファイル

### 5-1. `configs/server.yaml`

```yaml
control_port: 4610
auth_token: "change-me-to-a-secure-token"
port_range:
  min: 10000
  max: 20000
log_level: "info"
```

### 5-2. `configs/client.yaml`

```yaml
server: "your-vps-ip:4610"
auth_token: "change-me-to-a-secure-token"
tunnels:
  - local_port: 3000
    remote_port: 0
    protocol: "tcp"
log_level: "info"
```

---

## Phase 6: ローカルテスト

### 6-1. テスト用ローカルサーバーの起動

```bash
# ターミナル 1: テスト対象サービス (Python の簡易 HTTP サーバー)
python -m http.server 3000
```

### 6-2. サーバーの起動

```bash
# ターミナル 2:
go run ./cmd/soralink-server --config configs/server.yaml
```

期待出力:

```
level=INFO msg="server started" control_port=4610
```

### 6-3. クライアントの起動

```bash
# ターミナル 3:
go run ./cmd/soralink-client --server localhost:4610 --token change-me-to-a-secure-token --local 3000
```

期待出力:

```
level=INFO msg="connected to server" addr="localhost:4610"
level=INFO msg="tunnel established" remote_port=10042 url="localhost:10042" local_port=3000
```

### 6-4. 動作確認

```bash
# ターミナル 4:
curl http://localhost:10042
```

Python HTTP サーバーのファイル一覧が表示されれば成功。

### 6-5. 自動テスト実行

```bash
go test -v -race ./...
```

---

## Phase 7: VPS へのデプロイ

### 7-1. クロスコンパイル

```bash
# Linux AMD64 向けにビルド
make build-linux
```

### 7-2. ファイル転送

```bash
scp bin/soralink-server-linux user@<VPS_IP>:/usr/local/bin/soralink-server
scp configs/server.yaml user@<VPS_IP>:/etc/soralink/server.yaml
scp deploy/soralink.service user@<VPS_IP>:/etc/systemd/system/soralink.service
```

### 7-3. VPS 上のセットアップ

```bash
# SSH でサーバーに入る
ssh user@<VPS_IP>

# 実行権限付与
sudo chmod +x /usr/local/bin/soralink-server

# 設定ディレクトリ
sudo mkdir -p /etc/soralink

# トークンを安全な値に変更
sudo nano /etc/soralink/server.yaml
# auth_token を十分に長いランダム文字列に変更

# 専用ユーザー作成 (root で動かさない)
sudo useradd -r -s /bin/false soralink

# systemd 登録 & 起動
sudo systemctl daemon-reload
sudo systemctl enable soralink
sudo systemctl start soralink

# ログ確認
sudo journalctl -u soralink -f
```

### 7-4. `deploy/soralink.service`

```ini
[Unit]
Description=Soralink Relay Server
After=network.target

[Service]
Type=simple
User=soralink
ExecStart=/usr/local/bin/soralink-server --config /etc/soralink/server.yaml
Restart=always
RestartSec=5
LimitNOFILE=65536

# セキュリティ設定
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=/etc/soralink

[Install]
WantedBy=multi-user.target
```

### 7-5. ファイアウォール設定

```bash
# 制御ポート
sudo ufw allow 4610/tcp

# パブリックポート範囲
sudo ufw allow 10000:20000/tcp

# 確認
sudo ufw status
```

### 7-6. 本番テスト

```bash
# ローカルマシンでクライアント起動
go run ./cmd/soralink-client --server <VPS_IP>:4610 --token <your-token> --local 3000

# 別マシン or スマホから確認
curl http://<VPS_IP>:<割り当てポート>
```

---

## 完了チェックリスト

- [ ] `go test -v -race ./...` が ALL PASS
- [ ] `go vet ./...` がエラーなし
- [ ] ローカルで server ↔ client ↔ テストサービスが疎通
- [ ] VPS 上で server が systemd で動作
- [ ] 外部ネットワークから VPS 経由でローカルサービスにアクセスできる
- [ ] SIGTERM で server が graceful shutdown する
- [ ] クライアント切断時にサーバーのリソースがクリーンアップされる
- [ ] 認証トークンが不一致のとき接続が拒否される

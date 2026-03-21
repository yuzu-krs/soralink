# Soralink ロードマップ & 実装手順書

> **ゴール:** ngrok のようなフル機能トンネルサービスをセルフホストで実現する  
> **現在地:** MVP (Phase 0〜7) 完了 — TCP トンネルが VPS 経由で動作確認済み  
> **進め方:** 安定性 → セキュリティ → プロトコル拡張 → HTTP/HTTPS → 管理機能 → UX → 商用化

---

## 全体マップ

```
Phase A: 安定性強化          ← 今の TCP トンネルを「落ちない」レベルにする
Phase B: セキュリティ強化    ← インターネット公開に耐えるセキュリティ
Phase C: プロトコル拡張      ← TCP 以外（UDP / SSH / ゲームサーバー）対応
Phase D: HTTP/HTTPS トンネル ← サブドメイン + TLS 自動化 + WebSocket
Phase E: 管理機能 & 可観測性 ← REST API / メトリクス / ダッシュボード
Phase F: UX & CLI 強化       ← ngrok ライクなターミナル UI / ワンライナー
Phase G: マルチテナント & 商用化 ← ユーザー管理 / プラン / 課金 / Web UI
```

---

## Phase A: 安定性強化

**目的:** 長時間稼働で落ちないリレーサーバーにする  
**前提:** MVP のコードベースがそのまま動く状態

### A-1. Ping/Pong ヘルスチェック（サーバー主導）

**概要:** 制御コネクションの死活監視。半開き接続を検出して切断する。

**なぜ必要か:**
- クライアントの Wi-Fi 切断・ノート PC スリープ等で TCP が半開きになる
- サーバー側はそれに気づけず、ゾンビトンネルが残り続ける
- ポートが枯渇して新規トンネルが作れなくなる

**実装手順:**

1. **サーバー側 — 定期 Ping 送信**

   ```go
   go func() { // goroutine: ping ticker for client
       ticker := time.NewTicker(30 * time.Second)
       defer ticker.Stop()
       for {
           select {
           case <-ctx.Done():
               return
           case <-ticker.C:
               if err := protocol.WriteMessage(conn, protocol.MsgTypePing, nil); err != nil {
                   return
               }
           }
       }
   }()
   ```

2. **クライアント側 — Pong 返送**
   - `messageLoop` 内で `MsgTypePing` を受信したら即座に `MsgTypePong` を返す（MVP で対応済み）

3. **サーバー側 — タイムアウト検知**
   - `lastPong` に最後の Pong 受信時刻を記録
   - 60 秒間 Pong が来なかったら制御コネクションを切断
   - `RemoveByClient(conn)` で関連する全トンネルをクリーンアップ

4. **テスト:**
   - `net.Pipe()` で擬似クライアントを作り、Pong を返さないケースで 60 秒以内に切断されることを確認

### A-2. クライアント自動再接続

**概要:** 制御コネクション切断時に Exponential Backoff + Jitter で再接続。

**なぜ必要か:**
- サーバーの再起動、ネットワーク一時断でクライアントが死んだままでは使えない
- ngrok はクライアントが自動で再接続してトンネルを復活させる

**実装手順:**

1. **`Client.RunWithRetry()` を実装**

   ```go
   func (c *Client) RunWithRetry(ctx context.Context) error {
       backoff := 1 * time.Second
       maxBackoff := 30 * time.Second
       for {
           err := c.Run(ctx)
           if ctx.Err() != nil {
               return ctx.Err() // 明示的な終了
           }
           slog.Warn("disconnected, reconnecting", "err", err, "backoff", backoff)
           jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
           select {
           case <-ctx.Done():
               return ctx.Err()
           case <-time.After(backoff + jitter):
           }
           backoff = min(backoff*2, maxBackoff)
       }
   }
   ```

2. **`cmd/soralink-client/main.go` で `RunWithRetry()` を呼び出す**
3. **`Run()` 内のリソースを正しくクリーンアップ** — 制御コネクションを `defer conn.Close()`
4. **テスト:** サーバーを停止 → 再起動し、クライアントが自動でトンネルを再確立することを確認

### A-3. Graceful Shutdown の完全実装

**なぜ必要か:**
- `systemctl restart soralink` でサーバーを再起動したときに既存接続が即断されるのを防ぐ
- クライアントが予告なく切断されると再接続に無駄なリトライが発生する

**実装手順:**

1. **サーバー — `Shutdown(ctx context.Context) error`**
   - 新規接続の受付を停止 (`listener.Close()`)
   - 全クライアントに `MsgTypeError` (shutdown メッセージ) を送信
   - 全トンネルを順次クローズ
   - タイムアウト付き（10 秒）で既存接続の完了を待機

2. **クライアント — サーバーの shutdown 通知に応答**
   - `MsgTypeError` で "server shutting down" を受信したら即座に再接続ループに入る

### A-4. Read/Write デッドラインの徹底

**なぜ必要か:**
- デッドラインなしだと、相手が無応答のときに goroutine が永久ブロックする
- fd リークの主要原因

**実装手順:**

1. **制御コネクション:** Read 60 秒 / Write 10 秒
2. **データコネクション待機:** `NewConnection` 送信後、10 秒以内にデータコネクションが来なかったらタイムアウト
3. **ブリッジ中:** デッドラインは設定しない（データ転送は長時間続く）

### A-5. 転送量カウンター付き Bridge

**概要:** `io.Copy` をラップして送受信バイト数を記録する。

**なぜ必要か:**
- Phase E のメトリクスやアクセスログに転送量データが必要
- 将来の課金（Phase G）でも使う

**実装手順:**

```go
type countingWriter struct {
    w     io.Writer
    bytes atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
    n, err := cw.w.Write(p)
    cw.bytes.Add(int64(n))
    return n, err
}
```

---

## Phase B: セキュリティ強化

**目的:** インターネットに公開するサービスとして最低限のセキュリティを確保  
**前提:** Phase A 完了（安定して動く状態）

### B-1. TLS 暗号化（制御 + データコネクション）

**概要:** 制御コネクション / データコネクションすべてを TLS で保護。

**なぜ必要か:**
- 現状の auth_token は平文で流れている → スニッフ可能
- 転送データも平文 → 盗聴・改竄リスク

**実装手順:**

1. **証明書の準備**

   ```bash
   # 開発用: 自己署名証明書
   openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes
   ```

2. **サーバー側 — TLS Listener**

   ```go
   cert, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
   tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
   listener, err := tls.Listen("tcp", addr, tlsCfg)
   ```

3. **クライアント側 — TLS Dial**

   ```go
   tlsCfg := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify}
   conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
   ```

4. **設定追加:**

   ```yaml
   # server.yaml
   tls:
     cert: "/etc/soralink/cert.pem"
     key: "/etc/soralink/key.pem"
   
   # client.yaml
   tls_skip_verify: true  # 自己署名証明書用（本番では false）
   ```

5. **後方互換:** `tls` セクションがなければ平文 TCP にフォールバック（開発用）

### B-2. 接続レート制限

**概要:** 同一 IP からの接続数を制限し、DoS 攻撃を軽減。

**実装手順:**

1. `golang.org/x/time/rate` パッケージを使用
2. IP アドレスごとに `rate.Limiter` を管理
3. `acceptLoop` 内で Accept 直後にチェック、超過時は即 `conn.Close()`
4. 古い Limiter を定期的に GC（5 分間アクセスなし → 削除）
5. 設定:
   ```yaml
   rate_limit:
     per_ip: 10   # 1秒あたりの接続数
     burst: 20    # バースト許容
   ```

### B-3. トンネルごとのアクセス制御（IP ホワイトリスト）

**概要:** トンネルに接続可能な IP を制限する。

**実装手順:**

1. `MsgRequestTunnel` に `allowed_ips` フィールドを追加
2. `Tunnel.AcceptLoop` で Accept 後、リモート IP をチェック
3. ホワイトリスト外なら即 Close
4. クライアント設定:
   ```yaml
   tunnels:
     - local_port: 3000
       allowed_ips: ["203.0.113.0/24", "198.51.100.5"]
   ```

### B-4. 認証トークンの HMAC 署名化

**概要:** 平文トークン比較 → HMAC-SHA256 チャレンジレスポンスに進化。

**なぜ必要か:**
- 現状は auth_token をそのまま送信 → TLS なしだと即漏洩
- HMAC ならトークン自体がネットワークを流れない

**実装手順:**

1. サーバーがランダム nonce を送信
2. クライアントが `HMAC-SHA256(nonce, token)` を計算して返す
3. サーバーが同じ計算をして一致すれば認証成功
4. TLS と組み合わせて二重防護

---

## Phase C: プロトコル拡張（TCP 以外の対応）

**目的:** TCP 以外にも UDP / SSH / ゲームサーバーを公開可能にする  
**前提:** Phase B 完了（セキュアな通信基盤がある状態）

### C-1. SSH トンネル

**概要:** ローカルの SSH サーバーを VPS 経由で公開する。

**なぜ必要か:**
- リモートから自宅 PC に SSH したい（最も一般的なユースケース）
- ngrok でも `ngrok tcp 22` で対応しているパターン

**実装手順:**

1. **既存の TCP トンネルがそのまま使える** — SSH は TCP 上のプロトコル
2. クライアント設定:
   ```yaml
   tunnels:
     - local_port: 22
       protocol: "tcp"
       remote_port: 0
   ```
3. 外部からのアクセス:
   ```bash
   ssh user@<VPS_IP> -p <割り当てポート>
   ```
4. **テスト:** ローカルに `sshd` を立て、VPS 経由で SSH 接続を確認
5. **ドキュメントに使い方を追記** — SSH 用の設定例と注意事項

> **ポイント:** SSH 対応は追加コードゼロ。ドキュメントと設定例だけで完了する。

### C-2. UDP トンネル（ゲームサーバー対応の基盤）

**概要:** UDP パケットをリレーする仕組みを追加。

**なぜ必要か:**
- Minecraft (Bedrock Edition)、Valheim、ARK、Terraria 等のゲームサーバーは UDP を使う
- playit.gg の主要ユースケースが「ゲームサーバーの公開」

**設計方針:**
- UDP は TCP と異なりコネクションレスなので、制御コネクション経由でのマッチングは不要
- サーバー上で UDP ポートを Listen し、パケットをそのままクライアントに転送する
- 制御コネクション（TCP）内に UDP パケットをカプセル化して転送

**プロトコル拡張:**

```go
// 新しいメッセージタイプ
const (
    MsgTypeUDPData byte = 0x0B // UDP パケットの転送
)

// UDP パケットのカプセル化
type MsgUDPData struct {
    TunnelID   string `json:"tunnel_id"`
    RemoteAddr string `json:"remote_addr"` // 送信元アドレス
    Data       []byte `json:"data"`        // base64 エンコードされた UDP ペイロード
}
```

**実装手順:**

1. **`internal/server/udp.go` — UDP リスナー**
   ```go
   type UDPTunnel struct {
       ID         string
       conn       *net.UDPConn       // パブリック UDP ポート
       clientConn net.Conn           // 制御コネクション (TCP)
       clients    map[string]*net.UDPAddr // リモートアドレス → UDPAddr
       mu         sync.RWMutex
   }
   ```

2. **パケットフロー:**
   ```
   外部ユーザー (UDP) → VPS :15000 (UDP Listen)
     → MsgUDPData として制御コネクション (TCP) 経由でクライアントに送信
     → クライアントが localhost:25565 (UDP) に転送
     → 応答を逆方向に転送
   ```

3. **クライアント設定:**
   ```yaml
   tunnels:
     - local_port: 25565
       protocol: "udp"
       remote_port: 0
   ```

4. **制限事項と対策:**
   - TCP カプセル化による遅延増加 → 遅延が問題なら将来 QUIC ベースに
   - UDP パケットサイズ上限: 65535 バイト → `MaxPayloadSize` を超えないかチェック
   - NAT タイムアウト: 60 秒間パケットがなかったセッションを GC

5. **テスト:**
   - `net.ListenUDP` + `net.DialUDP` でローカル UDP echo サーバーを経由するテスト

### C-3. ゲームサーバー特化機能

**概要:** Minecraft / Valheim / ARK 等のゲームサーバーを簡単に公開する。

**なぜ必要か:**
- playit.gg の主要ターゲット
- ゲーマーはネットワーク知識が少ない → 「簡単に公開できる」が差別化要素

**実装手順:**

1. **ゲームプリセット設定**
   ```yaml
   tunnels:
     - preset: "minecraft-java"     # TCP 25565
     - preset: "minecraft-bedrock"  # UDP 19132
     - preset: "valheim"            # UDP 2456-2458 (3ポート)
     - preset: "terraria"           # TCP 7777
     - preset: "ark"                # UDP 7777 + TCP 27015
   ```

2. **プリセット定義ファイル: `internal/client/presets.go`**
   ```go
   var gamePresets = map[string][]TunnelConfig{
       "minecraft-java": {
           {LocalPort: 25565, Protocol: "tcp"},
       },
       "minecraft-bedrock": {
           {LocalPort: 19132, Protocol: "udp"},
       },
       "valheim": {
           {LocalPort: 2456, Protocol: "udp"},
           {LocalPort: 2457, Protocol: "udp"},
           {LocalPort: 2458, Protocol: "udp"},
       },
       "terraria": {
           {LocalPort: 7777, Protocol: "tcp"},
       },
       "ark": {
           {LocalPort: 7777, Protocol: "udp"},
           {LocalPort: 27015, Protocol: "tcp"},
       },
   }
   ```

3. **CLI でワンライナー起動:**
   ```bash
   soralink-client --preset minecraft-java
   soralink-client --preset valheim
   ```

4. **ポート範囲トンネル** — 一部のゲームは連続ポートが必要
   ```go
   type TunnelConfig struct {
       LocalPort  int    `yaml:"local_port"`
       LocalPorts string `yaml:"local_ports"` // "2456-2458" の範囲指定
       Protocol   string `yaml:"protocol"`
   }
   ```

### C-4. TCP + UDP 同時トンネル

**概要:** 同じポート番号で TCP と UDP の両方を公開する。

**なぜ必要か:**
- ARK は同じポートで TCP と UDP の両方を使う
- ゲームによっては TCP（ログイン）と UDP（ゲームプレイ）を併用

**実装手順:**

1. `protocol` フィールドに `"both"` を許可
2. サーバー側で同じポート番号に TCP Listener + UDP Conn を作成
3. `TunnelManager` で TCP/UDP を別々に管理しつつ、ポート番号を共有

---

## Phase D: HTTP/HTTPS トンネル & サブドメインルーティング

**目的:** `myapp.tunnel.yourdomain.com` でアクセス可能にする（ngrok の核心機能）  
**前提:** Phase C 完了（複数プロトコル対応済み）

### D-1. HTTP リバースプロキシ

**概要:** ポート 80 で Listen し、Host ヘッダーでトンネルを特定してルーティング。

**なぜ必要か:**
- ポート番号でのアクセス（`:10000`）はユーザーに見せづらい
- `myapp.tunnel.soralink.io` なら URL を共有しやすい
- ngrok が人気な最大の理由がこのサブドメイン機能

**アーキテクチャ:**
```
ブラウザ → myapp.tunnel.soralink.io:443 → VPS (HTTP Proxy)
    → Host ヘッダーで "myapp" を抽出
    → TunnelManager から該当トンネルを取得
    → クライアントにデータコネクション要求
    → ローカル :3000 に転送
```

**実装手順:**

1. **新しいパッケージ: `internal/server/httpproxy/`**

2. **`httpproxy.go` — HTTP プロキシサーバー**

   ```go
   type HTTPProxy struct {
       tunnels  *server.TunnelManager
       domain   string // "tunnel.soralink.io"
       logger   *slog.Logger
   }

   func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
       subdomain := extractSubdomain(r.Host, p.domain)
       tunnel, ok := p.tunnels.GetBySubdomain(subdomain)
       if !ok {
           http.Error(w, "tunnel not found", http.StatusNotFound)
           return
       }
       proxy := &httputil.ReverseProxy{
           Director: func(req *http.Request) {
               req.URL.Scheme = "http"
               req.URL.Host = fmt.Sprintf("localhost:%d", tunnel.LocalPort)
               req.Header.Set("X-Forwarded-For", r.RemoteAddr)
               req.Header.Set("X-Forwarded-Proto", r.URL.Scheme)
           },
       }
       proxy.ServeHTTP(w, r)
   }
   ```

3. **`MsgRequestTunnel` を拡張:**

   ```go
   type MsgRequestTunnel struct {
       LocalPort     int    `json:"local_port"`
       Protocol      string `json:"protocol"`       // "tcp", "udp", "http", "https"
       RequestedPort int    `json:"requested_port"`
       Subdomain     string `json:"subdomain"`      // "myapp" → myapp.tunnel.soralink.io
   }
   ```

4. **DNS 設定:**
   - ワイルドカード A レコード: `*.tunnel.soralink.io → VPS_IP`
   - 設定のドメイン名は YAML で指定:
     ```yaml
     http_proxy:
       enabled: true
       domain: "tunnel.soralink.io"
       port: 80
     ```

5. **X-Forwarded ヘッダーの付与:**
   - `X-Forwarded-For`: 元のクライアント IP
   - `X-Forwarded-Proto`: http or https
   - `X-Forwarded-Host`: 元の Host ヘッダー

### D-2. HTTPS 対応 (Let's Encrypt 自動証明書)

**概要:** `*.tunnel.soralink.io` のワイルドカード証明書を自動取得・更新。

**実装手順:**

1. **`golang.org/x/crypto/acme/autocert`** で証明書管理
2. **DNS-01 チャレンジ** — ワイルドカード証明書には必須
   - DNS プロバイダの API（Cloudflare / Route53 等）で TXT レコードを自動追加
   - `lego` ライブラリが多数のプロバイダに対応
3. **HTTP → HTTPS 自動リダイレクト**
   ```go
   go http.ListenAndServe(":80", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
       http.Redirect(w, r, "https://"+r.Host+r.RequestURI, http.StatusMovedPermanently)
   }))
   ```
4. **証明書キャッシュ:** `/var/lib/soralink/certs/` にファイルキャッシュ
5. **設定:**
   ```yaml
   https:
     enabled: true
     email: "admin@soralink.io"          # Let's Encrypt 通知先
     dns_provider: "cloudflare"
     dns_api_token: "${CLOUDFLARE_API_TOKEN}"  # 環境変数から読み込み
     cache_dir: "/var/lib/soralink/certs"
   ```

### D-3. WebSocket サポート

**概要:** HTTP Upgrade ヘッダーを正しくハンドリングし、リアルタイム通信を中継。

**なぜ必要か:**
- チャットアプリ、リアルタイムダッシュボード、Socket.IO 等で必須
- HTTP プロキシが Upgrade を正しく扱わないと接続が切れる

**実装手順:**

1. `httputil.ReverseProxy` の `FlushInterval` を `-1` に設定（ストリーミング対応）
2. Upgrade ヘッダーが `websocket` の場合:
   - `http.Hijacker` インターフェースで生 TCP に降格
   - 降格後は既存の `Bridge()` で双方向コピー
3. テスト: `gorilla/websocket` の echo サーバーを経由して WebSocket 通信を確認

### D-4. カスタムドメイン対応

**概要:** `myapp.example.com` のような独自ドメインでトンネルにアクセス。

**実装手順:**

1. ユーザーが CNAME レコードを設定: `myapp.example.com → tunnel.soralink.io`
2. サーバー側でカスタムドメイン → トンネル ID のマッピングを管理
3. Let's Encrypt で個別証明書を HTTP-01 チャレンジで取得
4. `autocert.Manager.HostPolicy` でホワイトリスト管理

---

## Phase E: 管理機能 & 可観測性

**目的:** 運用に必要な監視・管理・ログ基盤を整備  
**前提:** Phase D 完了（HTTP/HTTPS トンネルが動く状態）

### E-1. REST API（管理用）

**概要:** アクティブなトンネル一覧・強制切断等の管理 API。

**エンドポイント:**

| Method   | Path                | 説明                          |
| -------- | ------------------- | ----------------------------- |
| `GET`    | `/api/tunnels`      | アクティブトンネル一覧        |
| `GET`    | `/api/tunnels/:id`  | トンネル詳細（転送量・接続数）|
| `DELETE` | `/api/tunnels/:id`  | トンネル強制切断              |
| `GET`    | `/api/clients`      | 接続中クライアント一覧        |
| `GET`    | `/api/stats`        | サーバー統計                  |
| `GET`    | `/api/health`       | ヘルスチェック                |

**実装手順:**

1. `internal/server/api/` パッケージを作成
2. 認証: Admin トークンで保護（`Authorization: Bearer <admin_token>`）
3. 標準ライブラリの `net/http` + `http.ServeMux` で実装（フレームワーク不要）
4. 管理ポートは別ポート（例: 4611）で Listen — 外部公開しない
5. レスポンスは JSON:
   ```json
   {
     "tunnels": [
       {
         "id": "a1b2c3d4",
         "remote_port": 10000,
         "protocol": "tcp",
         "subdomain": "myapp",
         "client_addr": "203.0.113.5:52341",
         "created_at": "2026-03-21T21:26:02Z",
         "bytes_sent": 1048576,
         "bytes_received": 524288,
         "active_connections": 3
       }
     ]
   }
   ```

### E-2. Prometheus メトリクス

**概要:** `prometheus/client_golang` でメトリクスを公開。

**メトリクス一覧:**

| メトリクス                          | 種別      | 説明                 |
| ----------------------------------- | --------- | -------------------- |
| `soralink_active_tunnels`           | Gauge     | アクティブトンネル数 |
| `soralink_active_connections`       | Gauge     | アクティブ接続数     |
| `soralink_bytes_transferred_total`  | Counter   | 転送バイト数（方向別）|
| `soralink_connections_total`        | Counter   | 累計接続数           |
| `soralink_auth_failures_total`      | Counter   | 認証失敗数           |
| `soralink_tunnel_duration_seconds`  | Histogram | トンネル存続時間     |
| `soralink_data_conn_latency_seconds`| Histogram | データコネクション確立遅延 |

**実装手順:**

1. `go get github.com/prometheus/client_golang`
2. `internal/server/metrics.go` にメトリクス定義
3. `/metrics` エンドポイントを管理 API ポートで公開
4. Grafana ダッシュボード JSON テンプレートを `deploy/grafana/` に配置

### E-3. 構造化アクセスログ

**概要:** 接続ごとの詳細ログを構造化出力。

```go
slog.Info("connection completed",
    "tunnel_id", tunnelID,
    "connection_id", connID,
    "protocol", protocol,
    "remote_addr", remoteAddr,
    "bytes_sent", bytesSent,
    "bytes_received", bytesReceived,
    "duration", duration,
)
```

**実装手順:**

1. `Bridge()` の戻り値にバイト数と所要時間を含める（Phase A-5 の `countingWriter` を活用）
2. 接続完了時にログ出力
3. 将来的にファイル出力・ログローテーションに対応

### E-4. リアルタイムイベントストリーム

**概要:** Server-Sent Events (SSE) でリアルタイムにトンネル状態を配信。

**実装手順:**

1. 管理 API に `GET /api/events` エンドポイントを追加
2. イベント種別: `tunnel.created`, `tunnel.closed`, `connection.opened`, `connection.closed`
3. クライアント CLI やダッシュボードでリアルタイム表示に活用

---

## Phase F: UX & CLI 強化

**目的:** ngrok のような直感的な CLI 体験を提供  
**前提:** Phase E 完了（管理 API がある状態）

### F-1. ターミナルダッシュボード（TUI）

**概要:** ngrok のようなリアルタイムステータス表示。

```
╔══════════════════════════════════════════════════════════╗
║  Soralink v0.2.0                         Status: online ║
╠══════════════════════════════════════════════════════════╣
║                                                          ║
║  Account:   free                                         ║
║  Forwarding:                                             ║
║    https://myapp.tunnel.soralink.io → localhost:3000     ║
║    tcp://soralink.io:10042          → localhost:22       ║
║                                                          ║
║  Connections:  42                                         ║
║  Bytes In:     12.5 MB                                   ║
║  Bytes Out:    8.3 MB                                    ║
║                                                          ║
║  HTTP Requests                                           ║
║  ───────────────────────────────────────────────────     ║
║  GET  /api/users      200  12ms                          ║
║  POST /api/login      401  8ms                           ║
║  GET  /assets/app.js  200  3ms                           ║
║                                                          ║
╚══════════════════════════════════════════════════════════╝
```

**実装手順:**

1. `github.com/charmbracelet/bubbletea` (TUI フレームワーク) を使用
2. `internal/client/tui/` パッケージを作成
3. リアルタイム更新: 管理 API の SSE エンドポイント or ローカルカウンター
4. HTTP リクエストのインスペクション表示（Phase D の HTTP プロキシのログを活用）

### F-2. ワンライナー起動の充実

**概要:** 設定ファイルなしでよく使うパターンを即座に実行。

```bash
# HTTP トンネル
soralink http 3000
soralink http 3000 --subdomain myapp

# TCP トンネル（SSH）
soralink tcp 22

# UDP トンネル（ゲームサーバー）
soralink udp 25565

# ゲームプリセット
soralink minecraft
soralink valheim
```

**実装手順:**

1. `cmd/soralink-client/main.go` にサブコマンドパーサーを追加
2. 第 1 引数がプロトコル or ゲーム名の場合、该当する `TunnelConfig` を自動構築
3. `--server` と `--token` はデフォルト設定ファイル (`~/.soralink/config.yaml`) から読み込み

### F-3. ローカル設定ファイル（ユーザーホーム）

**概要:** `~/.soralink/config.yaml` にデフォルト接続先を保存。

```yaml
# ~/.soralink/config.yaml
server: "tunnel.soralink.io:4610"
auth_token: "your-token-here"
```

**実装手順:**

1. `soralink auth <token>` コマンドで `~/.soralink/config.yaml` を生成
2. 以降はトークン指定不要で `soralink http 3000` だけで動く
3. プロジェクトディレクトリに `.soralink.yaml` があればそちらを優先

### F-4. HTTP リクエストインスペクター

**概要:** ngrok の Web インスペクターのように、トンネルを通過する HTTP リクエスト/レスポンスを確認。

**実装手順:**

1. HTTP プロキシを通過するリクエスト/レスポンスをリングバッファに保存（直近 100 件）
2. `GET /api/inspect/:tunnel_id` でリクエスト一覧を取得
3. リクエストヘッダー、ボディ、レスポンスステータス、レイテンシを記録
4. TUI ダッシュボードにリアルタイム表示
5. **プライバシー:** デフォルト OFF、`--inspect` フラグで有効化

---

## Phase G: マルチテナント & 商用化

**目的:** ngrok のように複数ユーザーが使える SaaS にする  
**前提:** Phase F 完了（十分な機能と安定性がある状態）

### G-1. ユーザー管理 & 認証

**概要:** ユーザー登録・ログイン・API キー管理。

**実装手順:**

1. **データベース:** SQLite（セルフホスト向け）or PostgreSQL（SaaS 向け）
2. **`internal/server/auth/`** パッケージ
3. ユーザーモデル:
   ```go
   type User struct {
       ID        string
       Email     string
       APIKey    string // クライアント認証用
       Plan      string // "free", "pro", "enterprise"
       CreatedAt time.Time
   }
   ```
4. API キーでの認証（現在の auth_token をユーザーごとの API キーに置き換え）
5. **セルフホスト時は単一ユーザーモード（現状維持）も選択可能**

### G-2. プラン & 制限

**概要:** ユーザーのプランに応じてトンネル数・接続数・転送量を制限。

| 制限項目           | Free     | Pro       | Enterprise |
| ------------------ | -------- | --------- | ---------- |
| 同時トンネル数     | 1        | 10        | 無制限     |
| 同時接続数/トンネル | 20       | 100       | 無制限     |
| 転送量/月          | 1 GB     | 100 GB    | 無制限     |
| カスタムサブドメイン | ×       | ○         | ○         |
| カスタムドメイン   | ×        | ×         | ○         |
| IP ホワイトリスト  | ×        | ○         | ○         |
| HTTP インスペクター| 直近 10  | 直近 100  | 無制限     |

**実装手順:**

1. `internal/server/quota/` パッケージ
2. トンネル作成時にクォータチェック
3. 転送量はリアルタイムカウント（Phase A-5 の `countingWriter`）
4. 月次リセット用の cron ジョブ or タイマー

### G-3. Web ダッシュボード

**概要:** ブラウザでトンネルの状態確認・設定変更・課金管理ができる。

**実装手順:**

1. **フロントエンド:** React or Svelte（`web/` ディレクトリ）
2. **API:** Phase E-1 の REST API を拡張
3. **ページ構成:**
   - ダッシュボード（アクティブトンネル一覧、転送量グラフ）
   - トンネル詳細（接続状況、HTTP リクエストログ）
   - 設定（API キー管理、プロフィール）
   - 課金（プラン変更、支払い履歴）
4. **組み込みサーブ:** Go バイナリに `embed.FS` で静的ファイルを埋め込む
   ```go
   //go:embed web/dist/*
   var webAssets embed.FS
   ```

### G-4. 課金システム（Stripe 連携）

**概要:** Pro / Enterprise プランの月額課金。

**実装手順:**

1. Stripe Checkout で支払いフロー
2. Stripe Webhook でサブスクリプション状態を同期
3. `internal/server/billing/` パッケージ
4. プランの変更がリアルタイムでクォータに反映

### G-5. リージョン分散

**概要:** 複数の VPS リージョンでトンネルを提供し、レイテンシを最小化。

**実装手順:**

1. **リージョン選択:** クライアントが最寄りのサーバーを選択
   ```bash
   soralink http 3000 --region ap-northeast-1
   ```
2. **エッジサーバー + コントロールプレーン分離:**
   - コントロールプレーン: ユーザー管理・課金（1 箇所）
   - エッジサーバー: 実際のトンネルリレー（各リージョン）
3. **ヘルスチェック & フェイルオーバー:** エッジサーバーがダウンしたら別リージョンに切り替え

---

## 実装優先度まとめ

```
【今すぐやるべき】
  Phase A (A-1 〜 A-4) — 安定性がないと長時間テストもできない

【次にやるべき】
  Phase B (B-1, B-2)   — TLS + レート制限で最低限のセキュリティ
  Phase C (C-1, C-2)   — SSH は即対応、UDP で差別化

【中期目標】
  Phase D (D-1 〜 D-3) — HTTP/HTTPS + サブドメインで ngrok に追いつく
  Phase E (E-1, E-2)   — 運用に必要な管理ツール

【長期目標】
  Phase F              — 使いやすい CLI で OSS コミュニティ獲得
  Phase G              — 商用化（SaaS 化する場合）
```

---

## 技術スタック見通し

| 領域               | 現在 (MVP)          | Phase D 完了時                  | Phase G 完了時                     |
| ------------------ | ------------------- | ------------------------------- | ---------------------------------- |
| プロトコル         | TCP のみ            | TCP + UDP + HTTP/HTTPS          | 同左 + QUIC (検討)                 |
| 暗号化             | なし                | TLS 1.3                         | 同左                               |
| 認証               | 共有トークン        | HMAC チャレンジ                 | ユーザー別 API キー                |
| 設定               | YAML                | YAML + CLI フラグ               | YAML + CLI + Web UI                |
| 監視               | slog のみ           | Prometheus + Grafana            | 同左 + アラート                    |
| データベース       | なし                | なし                            | SQLite / PostgreSQL                |
| フロントエンド     | なし                | なし                            | React / Svelte                     |
| デプロイ           | systemd             | systemd + Docker                | Kubernetes (オプション)            |
| 外部依存           | gopkg.in/yaml.v3    | + x/crypto, x/time, lego        | + database driver, Stripe SDK      |

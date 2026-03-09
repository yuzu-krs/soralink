# Soralink 将来機能ロードマップ & 実装手順書

> MVP 完成後に段階的に追加する機能群。  
> 顧客提供を見据え、安定性 → セキュリティ → ユーザビリティ → 商用化の順で進める。

---

## Phase A: 安定性強化

**目的:** 長時間稼働で落ちないリレーサーバーにする

### A-1. Ping/Pong ヘルスチェック

**概要:** 制御コネクションの死活監視。半開き接続を検出して切断する。

**実装手順:**

1. **サーバー側 — 定期 Ping 送信**

   ```
   go func() { // goroutine: ping ticker for client
       ticker := time.NewTicker(30 * time.Second)
       defer ticker.Stop()
       for {
           select {
           case <-ctx.Done():
               return
           case <-ticker.C:
               WriteMessage(conn, MsgTypePing, nil)
           }
       }
   }()
   ```

2. **クライアント側 — Pong 返送**
   - `messageLoop` 内で `MsgTypePing` を受信したら即座に `MsgTypePong` を返す

3. **サーバー側 — タイムアウト検知**
   - 最後の Pong 受信時刻を記録
   - 60 秒間 Pong が来なかったら制御コネクションを切断
   - 関連する全トンネルをクリーンアップ

4. **テスト:**
   - `net.Pipe()` で擬似クライアントを作り、Pong を返さないケースで切断を確認

### A-2. クライアント自動再接続

**概要:** 制御コネクション切断時に Exponential Backoff + Jitter で再接続。

**実装手順:**

1. **`Client.Run()` をリトライループでラップ**

   ```go
   func (c *Client) RunWithRetry(ctx context.Context) error {
       backoff := 1 * time.Second
       maxBackoff := 30 * time.Second
       for {
           err := c.Run(ctx) // 内部で接続→認証→メッセージループ
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

2. **`Run()` 内のリソースを正しくクリーンアップ**
   - 制御コネクションを `defer conn.Close()`
   - 再接続時にトンネルを再リクエスト

3. **テスト:**
   - サーバーを停止 → 再起動し、クライアントが自動でトンネルを再確立することを確認

### A-3. Graceful Shutdown の完全実装

**実装手順:**

1. **サーバー — `Shutdown(ctx context.Context) error`**
   - 新規接続の受付を停止 (`listener.Close()`)
   - 全クライアントに `MsgTypeError` (shutdown) を送信
   - 全トンネルを順次クローズ
   - タイムアウト付きで既存接続の完了を待機

2. **クライアント — サーバーの shutdown 通知に応答**
   - `MsgTypeError` (shutdown) を受信したら再接続ループに入る

### A-4. Read/Write デッドラインの徹底

**実装手順:**

1. **制御コネクション:**
   - Read: 60 秒 (Ping 間隔 30 秒の 2 倍)
   - Write: 10 秒

2. **データコネクション待機:**
   - `NewConnection` 送信後、10 秒以内にデータコネクションが来なかったらタイムアウト

3. **ブリッジ中:**
   - デッドラインは設定しない（データ転送は長時間続く）

### A-5. 複数トンネルの同時サポート

**概要:** 1 つの制御コネクションで複数ポートを公開。

**実装手順:**

1. クライアントの `Config.Tunnels` を配列にする（MVP で対応済み）
2. `requestTunnels()` でループして複数の `RequestTunnel` を送信
3. サーバー側で `tunnelID` → `Tunnel` のマップで管理
4. `NewConnection` に `tunnel_id` を含めてどのトンネル宛かを識別

---

## Phase B: セキュリティ強化

**目的:** インターネットに公開するサービスとしての最低限のセキュリティ

### B-1. TLS 暗号化

**概要:** 制御コネクション / データコネクションすべてを TLS で保護。

**実装手順:**

1. **証明書の準備**

   ```bash
   # 開発用: 自己署名証明書
   openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes
   ```

2. **サーバー側 — TLS Listener**

   ```go
   cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
   tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
   listener, err := tls.Listen("tcp", addr, tlsCfg)
   ```

3. **クライアント側 — TLS Dial**

   ```go
   tlsCfg := &tls.Config{
       InsecureSkipVerify: cfg.TLSSkipVerify, // 自己署名証明書用
   }
   conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
   ```

4. **設定追加:**

   ```yaml
   # server.yaml
   tls:
     cert: "/etc/soralink/cert.pem"
     key: "/etc/soralink/key.pem"
   ```

5. **テスト:** `tls` パッケージの `net.Pipe()` 相当で TLS 接続テスト

### B-2. Let's Encrypt 自動証明書

**概要:** `golang.org/x/crypto/acme/autocert` で自動取得。

**実装手順:**

1. ドメイン設定: `tunnel.yourdomain.com`
2. `autocert.Manager` で証明書取得
3. VPS で 80 ポートを ACME チャレンジ用に一時利用
4. 証明書はファイルキャッシュ (`/var/lib/soralink/certs/`)

### B-3. 接続レート制限

**概要:** 同一 IP からの接続を制限し、DoS 攻撃を軽減。

**実装手順:**

1. **`golang.org/x/time/rate`** パッケージを使用
2. IP アドレスごとに `rate.Limiter` を管理
   ```go
   type rateLimitMap struct {
       mu       sync.Mutex
       limiters map[string]*rate.Limiter
   }
   ```
3. `acceptLoop` 内で、Accept 直後にリミッターをチェック
4. 超過時は即座に `conn.Close()`
5. 設定:
   ```yaml
   rate_limit:
     per_ip: 10 # 1秒あたりの接続数
     burst: 20 # バースト許容
   ```

### B-4. トンネルごとのアクセス制御

**概要:** トンネルに接続可能な IP をホワイトリストで制限。

**実装手順:**

1. `MsgRequestTunnel` に `allowed_ips` フィールドを追加
2. `Tunnel.AcceptLoop` で Accept 後、リモート IP をチェック
3. ホワイトリスト外なら即 Close

---

## Phase C: HTTP トンネル & サブドメインルーティング

**目的:** ポート番号ではなく `username.tunnel.yourdomain.com` でアクセス可能にする

### C-1. HTTP リバースプロキシ

**概要:** ポート 80/443 で Listen し、Host ヘッダーでトンネルを特定してルーティング。

**実装手順:**

1. **新しいパッケージ: `internal/server/httpproxy/`**

2. **`httpproxy.go` — HTTP プロキシサーバー**

   ```go
   type HTTPProxy struct {
       tunnels  *TunnelManager
       listener net.Listener
       domain   string // "tunnel.yourdomain.com"
   }

   func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
       // Host ヘッダーから tunnel ID を抽出
       // 例: "abc123.tunnel.yourdomain.com" → tunnelID = "abc123"
       subdomain := extractSubdomain(r.Host, p.domain)
       tunnel, ok := p.tunnels.GetBySubdomain(subdomain)
       if !ok {
           http.Error(w, "tunnel not found", http.StatusNotFound)
           return
       }
       // httputil.ReverseProxy で転送
   }
   ```

3. **`MsgRequestTunnel` を拡張:**

   ```json
   {
     "local_port": 3000,
     "protocol": "http",
     "subdomain": "myapp"
   }
   ```

4. **DNS 設定:**
   - ワイルドカード A レコード: `*.tunnel.yourdomain.com → VPS_IP`

5. **テスト:**
   - `httptest.NewServer` と `httputil.ReverseProxy` を組み合わせたユニットテスト

### C-2. WebSocket サポート

**概要:** HTTP Upgrade ヘッダーを正しくハンドリング。

**実装手順:**

1. `httputil.ReverseProxy` の `FlushInterval` を `-1` に設定（ストリーミング対応）
2. Upgrade ヘッダーが `websocket` の場合、Hijacker インターフェースで生 TCP に降格
3. 降格後は既存の `Bridge()` で双方向コピー

### C-3. HTTPS 対応 (Let's Encrypt)

**実装手順:**

1. Phase B-2 の `autocert` を HTTP プロキシに統合
2. HTTP → HTTPS リダイレクト
3. ワイルドカード証明書: `*.tunnel.yourdomain.com`
   - ワイルドカードは DNS-01 チャレンジが必要
   - `lego` ライブラリ or DNS プロバイダの API で自動化

---

## Phase D: 管理機能 & 可観測性

**目的:** 運用に必要な監視・管理ツール

### D-1. REST API (管理用)

**概要:** アクティブなトンネル一覧・強制切断等の管理 API。

**実装手順:**

1. **`internal/server/api/`** パッケージを作成
2. 認証: Admin トークンで保護
3. エンドポイント:

   | Method   | Path               | 説明                   |
   | -------- | ------------------ | ---------------------- |
   | `GET`    | `/api/tunnels`     | アクティブトンネル一覧 |
   | `GET`    | `/api/tunnels/:id` | トンネル詳細           |
   | `DELETE` | `/api/tunnels/:id` | トンネル強制切断       |
   | `GET`    | `/api/clients`     | 接続中クライアント一覧 |
   | `GET`    | `/api/stats`       | サーバー統計           |

4. 標準ライブラリの `net/http` + `http.ServeMux` で実装 (フレームワーク不要)

### D-2. Prometheus メトリクス

**概要:** `prometheus/client_golang` でメトリクスを公開。

**メトリクス一覧:**

| メトリクス                         | 種別      | 説明                 |
| ---------------------------------- | --------- | -------------------- |
| `soralink_active_tunnels`          | Gauge     | アクティブトンネル数 |
| `soralink_active_connections`      | Gauge     | アクティブ接続数     |
| `soralink_bytes_transferred_total` | Counter   | 転送バイト数         |
| `soralink_connections_total`       | Counter   | 累計接続数           |
| `soralink_auth_failures_total`     | Counter   | 認証失敗数           |
| `soralink_tunnel_duration_seconds` | Histogram | トンネル存続時間     |

**実装手順:**

1. `go get github.com/prometheus/client_golang`
2. `internal/server/metrics.go` にメトリクス定義
3. `/metrics` エンドポイントを管理 API ポートで公開
4. Grafana ダッシュボード JSON テンプレートを `deploy/grafana/` に配置

### D-3. アクセスログ

**概要:** 接続ごとの詳細ログを構造化出力。

```go
slog.Info("connection completed",
    "tunnel_id", tunnelID,
    "connection_id", connID,
    "remote_addr", remoteAddr,
    "bytes_sent", bytesSent,
    "bytes_received", bytesReceived,
    "duration", duration,
)
```

**実装手順:**

1. `Bridge()` を拡張して `io.Copy` のバイト数を計上
   ```go
   type countingWriter struct {
       w     io.Writer
       count int64
   }
   func (cw *countingWriter) Write(p []byte) (int, error) {
       n, err := cw.w.Write(p)
       atomic.AddInt64(&cw.count, int64(n))
       return n, err
   }
   ```
2. 接続完了時にログ出力

### D-4. Web 管理ダッシュボード

**概要:** `htmx` + `templ` でシンプルな管理 UI。

**実装手順:**

1. **`internal/server/web/`** パッケージ
2. `templ` テンプレートエンジン: `go install github.com/a-h/templ/cmd/templ@latest`
3. ページ:
   - `/admin/` — ダッシュボード（トンネル数、接続数）
   - `/admin/tunnels` — トンネル一覧・切断ボタン
   - `/admin/clients` — クライアント一覧
4. 認証: Basic Auth or トークン認証
5. htmx で SPA ライクな UX（ページリロードなしでデータ更新）

---

## Phase E: マルチテナント & 商用化

**目的:** 複数の顧客にサービスとして提供する基盤

### E-1. ユーザー管理

**実装手順:**

1. **SQLite** (`modernc.org/sqlite`) でユーザー DB

   ```sql
   CREATE TABLE users (
       id TEXT PRIMARY KEY,
       email TEXT UNIQUE NOT NULL,
       password_hash TEXT NOT NULL,
       created_at DATETIME DEFAULT CURRENT_TIMESTAMP
   );

   CREATE TABLE tokens (
       id TEXT PRIMARY KEY,
       user_id TEXT REFERENCES users(id),
       token TEXT UNIQUE NOT NULL,
       name TEXT,
       created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
       expires_at DATETIME
   );
   ```

2. **`internal/server/auth/`** パッケージ
   - `Register(email, password) (*User, error)`
   - `Login(email, password) (*Token, error)`
   - `ValidateToken(token string) (*User, error)`

3. `Auth` メッセージのトークンを DB 照合に切り替え

### E-2. テナントごとのリソース制限

**実装手順:**

1. **テーブル追加:**

   ```sql
   CREATE TABLE plans (
       id TEXT PRIMARY KEY,
       name TEXT NOT NULL,
       max_tunnels INT DEFAULT 3,
       max_bandwidth_mb INT DEFAULT 1024,
       max_connections INT DEFAULT 100
   );

   ALTER TABLE users ADD COLUMN plan_id TEXT REFERENCES plans(id);
   ```

2. トンネル作成時にユーザーの残り枠をチェック
3. 帯域制限: `io.Copy` を `rate.Limiter` でラップ
   ```go
   type rateLimitedReader struct {
       r       io.Reader
       limiter *rate.Limiter
   }
   ```

### E-3. サブドメインの予約

**実装手順:**

1. `MsgRequestTunnel` で `subdomain` を指定
2. ユーザーに紐づくサブドメインを DB で管理
   ```sql
   CREATE TABLE subdomains (
       subdomain TEXT PRIMARY KEY,
       user_id TEXT REFERENCES users(id),
       tunnel_config JSON
   );
   ```
3. 他ユーザーとの重複チェック
4. 固定サブドメイン: `myapp.tunnel.yourdomain.com` が毎回同じ

### E-4. 課金統合

**概要:** Stripe API で従量課金。

**実装手順:**

1. `go get github.com/stripe/stripe-go/v76`
2. プラン（Free / Pro / Business）の定義
3. Webhook で支払いイベント受信
4. 帯域使用量の月次集計
5. 上限超過時の警告・制限

### E-5. CLI ツールの拡充

```bash
# ログイン
soralink login --email user@example.com

# トークン管理
soralink token create --name "dev-laptop"
soralink token list
soralink token revoke <token-id>

# トンネル管理
soralink tunnel list
soralink tunnel create --local 3000 --subdomain myapp

# ステータス
soralink status
```

---

## Phase F: 高可用性 & スケーリング

**目的:** 本番サービスとしての運用品質

### F-1. 複数 VPS での分散

**概要:** 複数リージョンの VPS でトンネルを提供。

**実装手順:**

1. クライアントが最も近いサーバーに自動接続
2. サーバー一覧を API で公開
3. GeoDNS or Anycast で最寄りにルーティング

### F-2. Redis による状態共有

**概要:** 複数サーバーインスタンス間でトンネル情報を共有。

**実装手順:**

1. `go get github.com/redis/go-redis/v9`
2. トンネルメタデータを Redis に保存
3. サブドメイン → サーバーインスタンスのルーティングテーブル

### F-3. UDP トンネル

**概要:** ゲームサーバー等の UDP トラフィックに対応。

**実装手順:**

1. `net.ListenPacket("udp", addr)` でパブリック UDP ポートを Listen
2. UDP は接続レスなため、送信元 IP:Port でセッションを管理
3. 一定時間パケットがなければセッション終了
4. クライアント側も `net.DialUDP` でローカルサービスに中継

---

## 優先度マトリクス

| Phase             | 重要度 | 顧客提供への必要性 | 推定工数 |
| ----------------- | ------ | ------------------ | -------- |
| A: 安定性強化     | ★★★★★  | 必須               | 2-3 日   |
| B: セキュリティ   | ★★★★★  | 必須               | 3-5 日   |
| C: HTTP トンネル  | ★★★★☆  | 強く推奨           | 5-7 日   |
| D: 管理機能       | ★★★☆☆  | 推奨               | 5-7 日   |
| E: マルチテナント | ★★★★★  | 商用化に必須       | 2-3 週間 |
| F: 高可用性       | ★★☆☆☆  | スケール時         | 3-4 週間 |

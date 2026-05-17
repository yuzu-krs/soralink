# Soralink ロードマップ

## 方針

Soralink はネットワーク基盤が中心の OSS サービスなので、最初に「つながる」「切れたら戻る」「漏れない」を固める。その後に Auth.js、Prisma、SQLite、dashboard、カスタムドメイン、Stripe 課金などの SaaS 機能を足す。

初期 Relay は開発者所有のグローバル IP 付き VPS 1 台で動かす。UDP は優先度低めとし、HTTP/TCP が安定してから扱う。

## Phase 0: 設計確定

目的:

- 要件定義、技術仕様、MVP 範囲を決める。
- Hosted SaaS と Self-hosted の優先順位を決める。
- 使用ドメイン、Relay の公開方式、トークン方式を決める。
- Auth.js GitHub OAuth、Prisma schema、SQLite 運用方針を決める。
- OSS としての secret 管理、`.env.example`、`SECURITY.md` の方針を決める。
- support / terms / privacy-policy / law の公開ページ方針を決める。
- Stripe のプラン構成と Webhook 同期方針を決める。

成果物:

- `docs/requirements.md`
- `docs/technical-spec.md`
- `docs/tech-stack.md`
- `docs/frontend-spec.md`
- `docs/roadmap.md`

## Phase 1: Core TCP Tunnel

目的:

- 最小構成で TCP トンネルを動かす。
- グローバル IP 付き VPS 1 台とローカル Agent で疎通できる状態にする。

機能:

- Go module 初期化
- `soralink relay` または `soralink-server`
- `soralink tcp <port>` または `soralink-client`
- token 認証
- Prisma `AgentToken` schema
- SQLite による token metadata 永続化
- TCP port 自動割り当て
- 外部 TCP connection の bridge
- Agent 切断時の cleanup
- `go test ./...`

完了条件:

- `localhost:22` など任意 TCP service を Relay の公開 port から利用できる。
- 不正 token が拒否される。
- Agent を止めると公開 port が閉じる。

## Phase 2: HTTP Tunnel

目的:

- ngrok らしい HTTP 公開体験を作る。

機能:

- HTTP reverse proxy
- wildcard domain routing
- ランダム subdomain
- `soralink http <port>`
- `X-Forwarded-*` header
- WebSocket support
- basic request log

完了条件:

- `soralink http 3000` で `https://<random>.soralink.dev` が表示される。
- ブラウザからアクセスして `localhost:3000` に届く。
- WebSocket echo が動く。

## Phase 3: TLS / HTTPS / 安定性

目的:

- 外部公開に耐える最低限の安全性と安定性を入れる。

機能:

- Relay endpoint の HTTPS
- Agent -> Relay の TLS
- heartbeat ping/pong
- Agent auto reconnect
- graceful shutdown
- read/write deadline
- rate limit
- 転送量カウント

完了条件:

- Relay 再起動後に Agent が自動復旧する。
- 長時間接続で goroutine / fd leak が発生しない。
- HTTP endpoint が HTTPS でアクセスできる。

## Phase 4: Auth.js / Dashboard MVP

目的:

- Auth.js の GitHub OAuth でログインし、Web で token を取得する体験を実現する。

機能:

- Auth.js GitHub OAuth
- Prisma Adapter
- SQLite database session
- token 発行/失効
- active tunnel 一覧
- tunnel 停止
- Prisma schema / migration
- userId scoped API query
- SQLite backup / restore 方針
- support / terms / privacy-policy / law の公開ページ

完了条件:

- Web で token を作成し、`soralink auth <TOKEN>` で使える。
- dashboard に active tunnel が表示される。
- 別ユーザーの token / tunnel metadata が API 経由で読めない。
- SQLite DB ファイルが repository に含まれない。
- 法務・サポート系の公開ページがログインなしで閲覧できる。

## Phase 5: Endpoint 管理

目的:

- 開発者が日常的に使いやすい URL/port 管理を提供する。

機能:

- 予約 subdomain
- 固定 TCP address
- IP allowlist
- Basic Auth
- カスタムドメイン
- Let's Encrypt 自動証明書

完了条件:

- `soralink http 3000 --subdomain myapp` が使える。
- `https://myapp.soralink.dev` を固定 URL として使える。
- 独自ドメインが CNAME 設定で使える。

## Phase 6: Observability / Inspection

目的:

- デバッグと運用に必要な可視化を入れる。

機能:

- request/response inspection
- 機密 header mask
- dashboard request log
- Prometheus metrics
- JSON structured logs
- admin health endpoint

完了条件:

- HTTP request の method/path/status/duration が dashboard と CLI に表示される。
- `/metrics` で active tunnel / connection / bytes が見える。

## Phase 7: SaaS 化

目的:

- 複数ユーザーに提供できるサービスへ拡張する。

機能:

- organization / team
- plan / quota
- usage 集計
- Free / Pro / Team / Enterprise plan 定義
- 定額 subscription の quota 制御
- Stripe Checkout
- Stripe Customer Portal
- Stripe Webhook 署名検証
- Stripe subscription と SQLite `BillingCustomer` の同期
- abuse detection
- PostgreSQL 移行検討
- multi-region Relay
- admin console

完了条件:

- Free / Pro などの plan に応じて tunnel 数や転送量を制限できる。
- MVP では従量課金を使わず、上限到達時に新規 tunnel / connection を制限できる。
- 請求状態と quota が連動する。
- SQLite の限界が見えた場合に PostgreSQL へ移行できる。
- 複数 Relay に tunnel を分散できる。

## Phase 8: UDP / ゲームサーバー

目的:

- UDP を必要とするゲームサーバー用途へ拡張する。

機能:

- UDP tunnel
- NAT session 管理
- Minecraft Bedrock、Valheim などの preset
- TCP/UDP 同時公開

完了条件:

- 代表的な UDP echo / game server preset が VPS Relay 経由で動作する。
- UDP session が timeout で cleanup される。

## 推奨する最初の実装順

1. `protocol` package
2. `relay` の control connection
3. token 認証
4. Prisma `AgentToken` schema
5. SQLite + Prisma migration
6. TCP port allocator
7. TCP bridge
8. Agent CLI の `tcp`
9. HTTP reverse proxy
10. Agent CLI の `http`
11. reconnect / heartbeat
12. HTTPS
13. Auth.js GitHub OAuth dashboard
14. Stripe billing

この順にすると、早い段階で「本当にトンネルとして機能するか」を確認できる。

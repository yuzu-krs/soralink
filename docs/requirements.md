# Soralink 要件定義

## 1. 概要

Soralink は、NAT やルーターのポート開放をせずに、ローカルマシン上の開発サーバーや TCP サービスを外部公開できるトンネルサービスである。

Soralink は OSS として開発する。ソースコード、Issue、Pull Request、デプロイ例が公開される前提で、秘匿情報をリポジトリに含めない設計と運用を必須とする。

ユーザー体験は次を基本形とする。

```bash
# Web ダッシュボードで取得したトークンを保存
soralink auth <TOKEN>

# HTTP 開発サーバーを公開
soralink http 3000

# SSH や DB など TCP サービスを公開
soralink tcp 22
```

公開例:

```text
https://blue-sky-123.soralink.dev -> http://localhost:3000
tcp://jp-1.soralink.dev:21432     -> localhost:22
```

## 2. 目的

- ローカル開発環境を一時的に外部公開できるようにする。
- Webhook の受信確認、スマホ実機確認、外部レビュー、デモ用途を簡単にする。
- TCP サービスも公開できるようにし、SSH、DB、ゲームサーバーなどへ拡張可能にする。
- 将来的には SaaS とセルフホストの両方に対応できる設計にする。
- OSS として安全に開発できるよう、キー管理、ログマスク、認可境界、DB ファイル保護を初期仕様に含める。

## 3. 対象ユーザー

| ユーザー | 主な用途 | 重要な価値 |
| --- | --- | --- |
| Web 開発者 | Webhook、OAuth callback、スマホ実機確認 | すぐ公開できる、HTTPS が使える |
| 個人開発者 | デモ共有、検証環境公開 | URL を共有しやすい、無料/低コスト |
| インフラ/バックエンド開発者 | SSH、DB、内部ツールの一時公開 | TCP 対応、アクセス制御 |
| ゲームサーバー運営者 | 自宅サーバー公開 | TCP/UDP 対応、ポート開放不要 |
| チーム/企業 | レビュー環境、社内検証 | 監査ログ、チーム管理、権限制御 |

## 4. 用語

| 用語 | 意味 |
| --- | --- |
| Agent | ユーザーのローカルマシンで動く `soralink` CLI/常駐プロセス |
| Relay / Edge | グローバル IP を持ち、外部通信を受ける Soralink サーバー |
| Control Plane | Web ダッシュボード、認証、トークン、課金、設定管理を担う API |
| Tunnel | 公開 endpoint とローカルサービスの対応関係 |
| Endpoint | 外部からアクセスする URL、ドメイン、TCP アドレス |
| Session | Agent と Relay の間に張られた認証済み接続 |
| Connection | 外部ユーザーから来た 1 本の HTTP/TCP 接続 |

## 5. 提供形態

### 5.1 Hosted SaaS

Soralink 側が Relay / Control Plane / Dashboard を運用する。ユーザーは GitHub OAuth でログイン後、Agent をインストールして使う。

### 5.2 Self-hosted

ユーザーが自分の VPS に Relay と Dashboard を立てる。初期開発ではこの構成を優先すると、マルチテナントなしで中核機能を検証できる。

### 5.3 初期インフラ前提

開発者はグローバル IP を持つ VPS を 1 台所有しているため、初期の Relay / Edge / Dashboard / SQLite DB はその VPS 上に構築する。

```mermaid
flowchart TD
    Internet["External User"] --> VPS["VPS Relay\nGlobal IP"]
    Agent["Local Agent / CLI"] --> VPS
    VPS --> Agent
    Agent --> Local["localhost:3000 / localhost:22"]
    Dashboard["Dashboard"] --> Auth["Auth.js\nGitHub OAuth"]
    Dashboard --> DB["SQLite\nPrisma"]
    Dashboard --> Stripe["Stripe\nCheckout + Webhooks"]
```

この構成では、Relay はトンネル転送を担当し、ユーザー認証は Auth.js、アカウント情報・トークン管理・課金メタデータは SQLite + Prisma、決済処理は Stripe を利用する。

## 6. 機能要件

### 6.1 アカウント・認証

| ID | 要件 | 優先度 |
| --- | --- | --- |
| AUTH-001 | ログイン機能は Auth.js を使用する | P1 |
| AUTH-002 | OAuth provider は GitHub のみ対応する | P1 |
| AUTH-003 | メールアドレス + パスワードの独自認証は実装しない | P1 |
| AUTH-004 | Dashboard は Auth.js session を使ってログイン状態を管理する | P1 |
| AUTH-005 | API は Auth.js の `auth()` で session を検証してユーザーを識別する | P1 |
| AUTH-006 | Auth.js の user/account/session は Prisma Adapter で SQLite に永続化する | P1 |
| AUTH-007 | ユーザーは Agent 用トークンを発行できる | P1 |
| AUTH-008 | Agent トークンは作成時のみ平文表示し、DB にはハッシュで保存する | P1 |
| AUTH-009 | Agent トークンは失効、再発行、名前付けができる | P2 |
| AUTH-010 | Agent トークンごとに接続元 IP 制限を設定できる | P3 |

### 6.2 DB / Prisma / 認可

| ID | 要件 | 優先度 |
| --- | --- | --- |
| DB-001 | DB は SQLite を使用する | P1 |
| DB-002 | ORM は Prisma を使用する | P1 |
| DB-003 | schema と migration は `prisma/schema.prisma` と Prisma Migrate で管理する | P1 |
| DB-004 | ユーザー所有データは `userId` を持ち、API/Prisma query で `session.user.id` に必ず絞る | P1 |
| DB-005 | `agentToken.secretHash` などの秘匿値は client response に含めない | P1 |
| DB-006 | Relay / backend の管理処理は内部 API secret で保護する | P1 |
| DB-007 | SQLite DB、WAL、backup は commit 禁止とする | P1 |
| DB-008 | SQLite backup / restore 手順を運用ドキュメント化する | P2 |
| DB-009 | 複数 writer、高負荷、複数リージョンが必要になったら PostgreSQL 移行を検討する | P3 |

### 6.3 CLI / Agent

| ID | 要件 | 優先度 |
| --- | --- | --- |
| CLI-001 | Go 製のクロスプラットフォーム CLI として配布する | P1 |
| CLI-002 | `soralink auth <TOKEN>` でローカル設定にトークンを保存できる | P1 |
| CLI-003 | `soralink http <PORT>` で HTTP トンネルを開始できる | P1 |
| CLI-004 | `soralink tcp <PORT>` で TCP トンネルを開始できる | P1 |
| CLI-005 | トンネル開始後、公開 URL/アドレスをターミナルに表示する | P1 |
| CLI-006 | 切断時に自動再接続し、トンネルを復旧する | P1 |
| CLI-007 | YAML 設定ファイルから複数トンネルを同時起動できる | P2 |
| CLI-008 | HTTP リクエスト履歴をターミナルで確認できる | P2 |
| CLI-009 | `soralink update` で自己更新できる | P3 |

### 6.4 HTTP / HTTPS トンネル

| ID | 要件 | 優先度 |
| --- | --- | --- |
| HTTP-001 | ローカル HTTP サーバーを公開 URL に転送できる | P1 |
| HTTP-002 | ランダムサブドメインを自動割り当てできる | P1 |
| HTTP-003 | HTTPS 終端を Relay 側で行える | P1 |
| HTTP-004 | WebSocket を中継できる | P1 |
| HTTP-005 | HTTP/2 クライアントからのアクセスに対応できる | P2 |
| HTTP-006 | 予約済みサブドメインを使える | P2 |
| HTTP-007 | カスタムドメインを CNAME で接続できる | P2 |
| HTTP-008 | Basic Auth / Bearer token / IP allowlist を endpoint に設定できる | P2 |
| HTTP-009 | HTTP リクエスト/レスポンスの inspection を任意で有効化できる | P2 |
| HTTP-010 | リクエストヘッダーの追加/削除/書き換えを設定できる | P3 |

### 6.5 TCP トンネル

| ID | 要件 | 優先度 |
| --- | --- | --- |
| TCP-001 | ローカル TCP ポートを Relay の公開 TCP アドレスへ転送できる | P1 |
| TCP-002 | Relay は利用可能な公開ポートを自動割り当てできる | P1 |
| TCP-003 | ユーザーは固定 TCP アドレスを予約できる | P2 |
| TCP-004 | TCP endpoint に IP allowlist を設定できる | P2 |
| TCP-005 | SSH、DB、任意の TCP サービスで動作する | P2 |
| TCP-006 | 同時接続数、転送量、接続時間を記録できる | P2 |

### 6.6 UDP トンネル

UDP は優先度低めとし、初期 MVP からは外す。ゲームサーバー用途を狙う場合の拡張機能とする。

| ID | 要件 | 優先度 |
| --- | --- | --- |
| UDP-001 | UDP パケットを Relay 経由でローカル UDP サービスへ転送できる | P3 |
| UDP-002 | Minecraft Bedrock、Valheim などのプリセットを提供できる | P3 |
| UDP-003 | NAT セッション管理とタイムアウト GC を実装する | P3 |

### 6.7 Web ダッシュボード

ページ構成と UI の詳細は [フロントエンド画面仕様](./frontend-spec.md) に定義する。

| ID | 要件 | 優先度 |
| --- | --- | --- |
| WEB-001 | ユーザーは現在の active tunnel を一覧できる | P1 |
| WEB-002 | Agent トークンを発行、失効、名前変更できる | P1 |
| WEB-003 | endpoint のアクセスログと転送量を確認できる | P2 |
| WEB-004 | サブドメイン予約、カスタムドメイン設定ができる | P2 |
| WEB-005 | トンネルを Web から強制停止できる | P2 |
| WEB-006 | チームメンバー、権限、監査ログを管理できる | P3 |
| WEB-007 | プラン、請求、利用量を確認できる | P3 |

### 6.8 管理 API

| ID | 要件 | 優先度 |
| --- | --- | --- |
| API-001 | REST API でトークン、endpoint、tunnel を管理できる | P2 |
| API-002 | Agent はトークンを使って Relay に認証できる | P1 |
| API-003 | 管理 API は Auth.js session / Agent token / internal secret で認証する | P1 |
| API-004 | API は OpenAPI 形式で仕様を出力できる | P3 |

### 6.9 可観測性

| ID | 要件 | 優先度 |
| --- | --- | --- |
| OBS-001 | 構造化ログを出力する | P1 |
| OBS-002 | active tunnel、接続数、転送量をメトリクス化する | P2 |
| OBS-003 | Prometheus `/metrics` を提供する | P2 |
| OBS-004 | HTTP request inspection はデフォルト OFF にする | P1 |
| OBS-005 | Cookie、Authorization などの機密ヘッダーはログでマスクする | P1 |

### 6.10 課金

| ID | 要件 | 優先度 |
| --- | --- | --- |
| BILL-001 | 課金は Stripe を使用する | P2 |
| BILL-002 | Checkout で有料プランへ加入できる | P2 |
| BILL-003 | Customer Portal で支払い方法、請求履歴、解約を管理できる | P2 |
| BILL-004 | Stripe Webhook を受け取り、subscription 状態を SQLite に同期する | P2 |
| BILL-005 | Webhook は Stripe signature を検証してから処理する | P1 |
| BILL-006 | プランに応じて tunnel 数、固定 subdomain、固定 TCP port、転送量を制限できる | P2 |
| BILL-007 | MVP の Hosted SaaS は Free / Pro / Team / Enterprise の 4 区分で設計する | P2 |
| BILL-008 | Free は 0円、Pro は 1,200円/月、Team は 4,800円/月を初期仮説とする | P2 |
| BILL-009 | MVP では転送量の従量課金を行わず、quota 到達時に新規 tunnel / connection を制限する | P1 |
| BILL-010 | Free の Hosted SaaS では TCP tunnel を標準開放せず、invite または有料プランで扱う | P1 |
| BILL-011 | Free plan は Stripe Product を作らず、DB 上の default plan として扱う | P2 |
| BILL-012 | Stripe price id は環境変数で管理し、client から渡された plan 名を信用しない | P1 |

初期プラン:

| 項目 | Free | Pro | Team | Enterprise |
| --- | ---: | ---: | ---: | --- |
| 月額 | 0円 | 1,200円 | 4,800円 | 個別見積 |
| seat | 1 | 1 | 5 | 個別 |
| active tunnel | 1 | 5 | 20 | 個別 |
| Agent token | 1 | 5 | 20 | 個別 |
| TCP tunnel | invite / disabled | yes | yes | yes |
| 予約 subdomain | 0 | 3 | 10 | 個別 |
| 固定 TCP port | 0 | 2 | 5 | 個別 |
| custom domain | 0 | 1 | 10 | 個別 |
| 月間転送量 | 5GB | 100GB | 1TB | 個別 |
| log 保持 | 24時間 | 7日 | 30日 | 個別 |

### 6.11 OSS 開発

| ID | 要件 | 優先度 |
| --- | --- | --- |
| OSS-001 | Soralink は OSS として公開開発する | P1 |
| OSS-002 | README に開発方針、ライセンス、セキュリティポリシーを記載する | P1 |
| OSS-003 | secret を含む `.env`、証明書秘密鍵、GitHub OAuth secret、Auth.js secret、Stripe secret、SQLite DB は commit 禁止とする | P1 |
| OSS-004 | `.env.example` にはダミー値のみ置く | P1 |
| OSS-005 | 脆弱性報告窓口として `SECURITY.md` を用意する | P2 |
| OSS-006 | PR では認可、Prisma query、ログ出力、secret 露出を重点レビューする | P1 |

### 6.12 公開ページ / 法務

| ID | 要件 | 優先度 |
| --- | --- | --- |
| PUBLIC-001 | `/` で未ログインユーザー向けホームページを公開する | P1 |
| PUBLIC-002 | `/terms` で利用規約を公開する | P1 |
| PUBLIC-003 | `/privacy-policy` でプライバシーポリシーを公開する | P1 |
| PUBLIC-004 | `/law` で特定商取引法に基づく表記を公開する | P1 |
| PUBLIC-005 | `/support` で問い合わせ先、GitHub Issues、セキュリティ報告導線を公開する | P2 |
| PUBLIC-006 | 法務系ページには最終更新日を表示する | P1 |
| PUBLIC-007 | 本番公開前に terms / privacy-policy / law の文面を事業形態に合わせて確認する | P1 |

## 7. 非機能要件

### 7.1 セキュリティ

- Agent と Relay の通信は TLS で暗号化する。
- Agent トークンは DB に平文保存しない。
- トークンは prefix + secret 形式にし、検索用 prefix と検証用 hash を分ける。
- Auth.js secret、GitHub OAuth secret、Stripe secret、SQLite DB は公開リポジトリに含めない。
- Dashboard / API は Auth.js session を検証し、ユーザー所有データは `userId` 条件で必ず絞る。
- Prisma Client は server-only module に閉じ込め、browser bundle に含めない。
- Stripe secret key と webhook signing secret は backend の環境変数に限定する。
- Stripe Webhook は署名検証に成功した event のみ処理する。
- endpoint には rate limit、同時接続数制限、IP allowlist を設定できる。
- HTTP inspection は opt-in とし、機密ヘッダー/ボディをマスクできる。
- abuse 対策として、無料プランの公開 URL にはレート制限と警告/ブロック機構を用意する。
- OSS 開発では secret scanning、依存関係スキャン、最小権限の CI secret を使う。

### 7.2 信頼性

- Agent はネットワーク切断時に exponential backoff + jitter で再接続する。
- Relay は Agent 切断時に関連 tunnel を必ず解放する。
- 半開き接続検出のため ping/pong heartbeat を持つ。
- graceful shutdown により新規接続受付停止、既存接続終了待ち、リソース解放を行う。

### 7.3 性能

- MVP では 1 Relay あたり 1,000 active tunnel、10,000 concurrent connection を目標値にする。
- HTTP/TCP 中継の追加レイテンシは同一リージョンで p95 50ms 未満を目標にする。
- 大容量転送時にメモリへ全 body を載せない。基本は stream copy とする。
- inspection 有効時も保存する body は上限を持つ。
- SQLite の write contention が問題になった場合は PostgreSQL へ移行する。

### 7.4 運用

- Relay は開発者所有のグローバル IP 付き VPS 上で Linux systemd / Docker により起動できる。
- 設定は YAML と環境変数で管理できる。
- ログは JSON 出力に対応する。
- ヘルスチェック endpoint を提供する。
- 証明書更新、Prisma migration、SQLite backup / restore 手順を運用ドキュメント化する。
- SQLite DB は永続 volume に置き、定期バックアップする。

### 7.5 配布

- CLI は Windows、macOS、Linux に対応する。
- Go の single binary として配布する。
- Homebrew、Scoop、GitHub Releases による配布を想定する。

## 8. MVP 範囲

最初の MVP は「Soralink Core」として、次だけを作る。

### 必須

- Go 製 `soralink` CLI
- Go 製 Relay server
- 開発者所有のグローバル IP 付き VPS 1 台で動く Relay
- Auth.js による GitHub OAuth ログイン
- SQLite + Prisma による user / token / tunnel metadata 管理
- API/Prisma query によるユーザーごとのデータ分離
- Agent token による認証
- TCP トンネル
- HTTP トンネル
- ランダムサブドメイン
- HTTPS 終端
- Agent 自動再接続
- basic な構造化ログ
- 単一ユーザー/単一テナント構成

### MVP ではやらない

- Stripe 課金の本番運用
- チーム管理
- UDP
- カスタムドメイン
- リッチな Web ダッシュボード
- request/response body の完全保存
- 複数リージョン

## 9. 受け入れ基準

MVP 完了の条件:

- `soralink auth <TOKEN>` で token が保存される。
- `soralink http 3000` を実行すると `https://<random>.soralink.dev` が表示される。
- 外部ブラウザからその URL にアクセスすると `localhost:3000` に到達する。
- WebSocket echo サーバーがトンネル越しに動く。
- `soralink tcp 22` を実行すると公開 TCP アドレスが表示される。
- 外部からその TCP アドレスへ接続すると `localhost:22` に到達する。
- Agent を停止すると endpoint は閉じられる。
- Relay を再起動すると Agent が自動再接続する。
- 不正 token では接続できない。
- GitHub OAuth で Dashboard にログインできる。
- 別ユーザーの token / tunnel metadata を Dashboard API 経由で取得できない。
- Relay ログに tunnel 作成、接続開始、接続終了、転送量が出る。
- `/` がログインなしで閲覧でき、ログイン導線と概要が表示される。
- `/terms`, `/privacy-policy`, `/law` がログインなしで閲覧できる。
- `/support` から問い合わせ先とセキュリティ報告導線に到達できる。

## 10. 参考にした公開情報

2026-05-17 時点で、ngrok の公開ドキュメントでは agent の authtoken 認証、HTTP/S endpoint、TCP endpoint、ランダム URL、固定 TCP address、独自ドメイン、Traffic Policy などが説明されている。Soralink はこれらを参考にしつつ、初期実装では HTTP/TCP トンネルに絞る。

- ngrok Agent: https://ngrok.com/docs/agent
- ngrok Agent CLI: https://ngrok.com/docs/agent/cli
- ngrok HTTP/S Endpoints: https://ngrok.com/docs/universal-gateway/http
- ngrok TCP Endpoints: https://ngrok.com/docs/universal-gateway/tcp
- Auth.js: https://authjs.dev/
- Auth.js GitHub Provider: https://authjs.dev/getting-started/providers/github
- Auth.js Prisma Adapter: https://authjs.dev/getting-started/adapters/prisma
- Prisma SQLite: https://www.prisma.io/docs/concepts/database-connectors/sqlite
- Prisma Migrate: https://docs.prisma.io/docs/cli/migrate
- Stripe Subscriptions: https://docs.stripe.com/payments/subscriptions
- Stripe Webhook Signatures: https://docs.stripe.com/webhooks/signatures

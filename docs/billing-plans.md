# Soralink 課金プラン仕様

## 1. 方針

Soralink は OSS と Hosted SaaS の両方を想定する。課金対象は Hosted SaaS の Relay、Dashboard、運用、サポートであり、OSS としてセルフホストする場合は Soralink 側の Stripe 課金対象にしない。

MVP では、Stripe の定額 subscription を使う。転送量の従量課金は初期実装では行わず、Soralink 側の quota で制限する。これにより、想定外の高額請求、未回収、Stripe usage 同期の複雑さを避ける。

## 2. プラン一覧

価格は初期仮説であり、正式公開前に VPS 原価、Stripe 手数料、サポート負荷、競合価格を見て再調整する。

| プラン | 月額 | 年額 | 対象 |
| --- | ---: | ---: | --- |
| Free | 0円 | 0円 | 個人開発、試用、OSS 体験 |
| Pro | 1,200円 | 12,000円 | 個人開発者、フリーランス、小規模開発 |
| Team | 4,800円 | 48,000円 | 小規模チーム、継続利用 |
| Enterprise | 個別見積 | 個別見積 | 専用 Relay、SLA、特殊要件 |

年額は約 2 か月分を割り引く想定とする。MVP では月額のみで開始し、年額は Stripe 運用に慣れてから追加してもよい。

## 3. 機能上限

| 項目 | Free | Pro | Team | Enterprise |
| --- | ---: | ---: | ---: | --- |
| seat 数 | 1 | 1 | 5 | 個別 |
| active tunnel | 1 | 5 | 20 | 個別 |
| Agent token | 1 | 5 | 20 | 個別 |
| HTTP/HTTPS tunnel | yes | yes | yes | yes |
| WebSocket | yes | yes | yes | yes |
| TCP tunnel | invite / disabled by default | yes | yes | yes |
| UDP tunnel | no | no | beta | 個別 |
| ランダム subdomain | yes | yes | yes | yes |
| 予約 subdomain | no | 3 | 10 | 個別 |
| 固定 TCP port | no | 2 | 5 | 個別 |
| custom domain | no | 1 | 10 | 個別 |
| 月間転送量 | 5GB | 100GB | 1TB | 個別 |
| 同時 connection 目安 | 20 | 200 | 1,000 | 個別 |
| connection log 保持 | 24時間 | 7日 | 30日 | 個別 |
| access control | basic only | Basic / Bearer / IP allowlist | team policy | 個別 |
| request inspection | limited | yes | yes | 個別 |
| support | community | email | priority email | 個別 |

Hosted Free で TCP tunnel を標準開放しないのは、踏み台、スキャン、迷惑通信、DDoS 反射の温床になりやすいため。セルフホスト版では利用者自身の VPS と責任範囲で TCP を有効化できる。

## 4. quota の扱い

MVP では超過課金をしない。上限到達時の挙動は次の通り。

| quota | 80% 到達 | 100% 到達 |
| --- | --- | --- |
| 月間転送量 | Dashboard に警告を表示 | 新規 tunnel 作成を拒否し、既存 tunnel は段階的に制限 |
| active tunnel | warning なし | 新規 tunnel 作成を拒否 |
| Agent token | warning なし | 新規 token 作成を拒否 |
| 同時 connection | Relay log に warning | 新規 connection を `429` または connection refused |

有料プランでは、初期運用に限り 100% 到達後も短い grace を設けることができる。ただし自動で追加課金しない。

## 5. Stripe 設計

### 5.1 Products

Stripe Dashboard には次の Product を作る。

| Product | 用途 |
| --- | --- |
| `Soralink Pro` | 個人向け有料プラン |
| `Soralink Team` | チーム向け有料プラン |
| `Soralink Enterprise` | 見積・請求書払い用の管理枠。MVP では実装しない |

Free は Stripe Product を作らず、Soralink DB 上の default plan として扱う。

### 5.2 Prices

MVP では monthly price を先に作成する。

| 環境変数 | Stripe Price | 金額 | interval |
| --- | --- | ---: | --- |
| `STRIPE_PRICE_PRO_MONTHLY` | Pro monthly | 1,200円 | month |
| `STRIPE_PRICE_TEAM_MONTHLY` | Team monthly | 4,800円 | month |
| `STRIPE_PRICE_PRO_YEARLY` | Pro yearly | 12,000円 | year |
| `STRIPE_PRICE_TEAM_YEARLY` | Team yearly | 48,000円 | year |

年額 price は作成しても、MVP 画面では非表示にしてよい。

### 5.3 metadata

Stripe Product / Price には次の metadata を設定する。

| key | 値の例 | 用途 |
| --- | --- | --- |
| `soralink_plan` | `pro` | Soralink の plan 判定 |
| `soralink_interval` | `month` | 月額/年額の識別 |
| `limits_version` | `v1` | quota 定義の世代管理 |

Soralink 側では price id を信用し、client から渡された plan 名だけでプラン変更しない。

## 6. Webhook 同期

受け取る Stripe event:

| event | 処理 |
| --- | --- |
| `checkout.session.completed` | `stripeCustomerId` と `stripeSubscriptionId` を紐づける |
| `customer.subscription.created` | plan / status / currentPeriodEnd を同期 |
| `customer.subscription.updated` | plan / status / currentPeriodEnd を更新 |
| `customer.subscription.deleted` | plan を `free` に戻す、または期間終了まで entitlement を維持 |
| `invoice.payment_failed` | `past_due` として grace / 制限の判定に使う |
| `invoice.paid` | `active` へ回復した場合に制限を解除 |

Webhook は raw body と `Stripe-Signature` を使って署名検証してから処理する。検証前に JSON parse しない。

## 7. status と entitlement

| Stripe status | Soralink entitlement |
| --- | --- |
| `trialing` | 対象 plan を有効 |
| `active` | 対象 plan を有効 |
| `past_due` | grace 中は有効、期限後は Free 相当に制限 |
| `unpaid` | Free 相当に制限 |
| `canceled` | Free 相当に制限 |
| `incomplete` | Free のまま |
| `incomplete_expired` | Free のまま |

MVP では free trial を設けない。Free plan が試用枠を兼ねる。

## 8. Dashboard 表示

`/app/billing` では次を表示する。

- 現在の plan
- subscription status
- 月間転送量と上限
- active tunnel / token / reserved endpoint の使用数
- plan 比較表
- Upgrade button
- Customer Portal button

Checkout から戻った直後は Webhook 反映前の可能性があるため、Dashboard は短時間 polling するか、手動 refresh 導線を置く。

## 9. 将来検討

- Stripe Meters API による usage-based billing
- 追加転送量パック
- 追加 seat
- dedicated Relay add-on
- annual plan の公開
- Stripe Tax
- 請求書払い
- Enterprise SLA

## 10. 参考

2026-05-17 時点で、Stripe は subscription、Checkout、Customer Portal、usage-based billing、Webhook signature verification を公式機能として提供している。Soralink MVP では Checkout / Customer Portal / Webhook を使い、usage-based billing は後続フェーズに回す。

- Stripe Billing Pricing: https://stripe.com/billing/pricing
- Stripe Checkout: https://stripe.com/payments/checkout
- Stripe Customer Portal: https://docs.stripe.com/billing/subscriptions/integrating-customer-portal
- Stripe Subscriptions: https://docs.stripe.com/payments/subscriptions
- Stripe Webhook Signatures: https://docs.stripe.com/webhooks/signatures

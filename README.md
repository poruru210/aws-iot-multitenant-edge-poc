# aws-iot-multitenant-edge-poc

Go のエッジデバイスと AWS CDK v2 / TypeScript による検証実装です。
**初回登録は MQTT over TLS、通常運用は HTTPS（IoT Credentials Provider と S3）**です。
AWS への自動デプロイは行いません。実AWSでの接続・認可検証は `docs/acceptance.md` に従い、検証専用アカウントで行ってください。

## 構成

```text
サービスマン: Go CLI + AWS IAM Identity Center等の一時認証情報
  └─ SigV4 → API Gateway（IAM認証・テナント別パス制限）
               └─ Claim発行Lambda → DynamoDB登録台帳 + CreateProvisioningClaim
                    └─ 5分限定のClaimを安全にデバイスへ受け渡し

デバイス: Go CLI（本番秘密鍵はデバイスで生成・外部送信しない）
  ├─ MQTT/TLS → Fleet Provisioning（CSR → RegisterThing）
  │                 └─ 登録前フック: Claim・公開鍵・所属・再利用を検証
  ├─ HTTPS/mTLS → IoT Credentials Provider → 15分の一時認証情報
  └─ HTTPS/SigV4 → S3 Multipart Upload
                   tenants/{ThingTypeName}/devices/{ThingName}/{relative-key}
```

デバイスの実装は Go、Lambda・インフラは TypeScript です。画面ではなく CLI で、既存システムが集約した**ローカルファイル**を入力にします。GUI、既存マシンからの収集機能、証明書の自動更新は今回の対象外です。

## 実装済みのコマンド

| コマンド | 用途 |
|---|---|
| `init` | P-256秘密鍵・CSR・公開鍵フィンガープリント付き登録要求の生成 |
| `claim` | 作業者のAWS認証情報で登録APIを呼び、一時Claimをファイルへ保存 |
| `provision` | MQTT/TLSの8883または443/ALPNでCSR提出・登録 |
| `credentials` | 本番証明書でSTS取得を確認。キー・トークンは表示しない |
| `upload` | S3直接Multipart、並列転送、SHA-256検証、途中再開 |
| `probe-denied` | 他テナント／他デバイスへの書き込み拒否を実AWSで検証 |

## 前提とビルド

Go 1.23以上、Node.js 22、npm、AWS CLI v2を用意します。コマンド例はbashです。
AWSに接続せずビルド・単体テスト・CloudFormation生成まで実行できます。

```bash
go mod tidy
go test -race ./...
go vet ./...
mkdir -p bin
go build -o bin/edge ./cmd/edge

cd infra
npm install
npm run build
npm test
npm run synth
cd ..
```

初回の依存解決にはインターネット接続が必要です。`go.sum` と `infra/package-lock.json` は依存解決で生成されます。CIでは解決結果を検証成果物へ保存します。運用開始前にはそれらをレビュー・コミットし、以後 `npm ci` と `go test -mod=readonly` へ切り替えてください。

## デプロイ（利用者が明示的に実行）

検証用AWSアカウントを使い、`infra/cdk.json` のテナントを確認してください。既定は `tenant-a`、`tenant-b`。Thing Type名はアカウント・リージョン内で共有されるため既存名と衝突させないでください。

```bash
aws sso login --profile poc-admin
export AWS_PROFILE=poc-admin
export AWS_REGION=ap-northeast-1

cd infra
npx cdk bootstrap aws://<AWS_ACCOUNT_ID>/ap-northeast-1
npx cdk diff
npx cdk deploy --outputs-file ../cdk-outputs.json
cd ..
```

`OperatorPolicy0Arn` は `tenant-a`、`OperatorPolicy1Arn` は `tenant-b` の登録API呼び出しポリシーです。許可するテナントのポリシーだけを、作業者のIAMロール・Identity Center権限セットへ適用します。**管理者権限のまま作業者のテナント制限を検証しないでください。** 人のログイン画面・Identity Center自体の構築は本スタックには含めません。

S3はSSE-S3、パブリックアクセス禁止、TLS必須です。データの自動期限削除は既定で**無効**（`retentionDays: 0`）。明示的に30等を設定した場合だけ有効になります。未完了Multipartのパートは1日後のライフサイクル削除対象です。

## 登録からアップロードまで

### 1. デバイスで秘密鍵とCSRを作る

```bash
./bin/edge init --dir .private/edge-001 --thing edge-001
```

`enrollment.json` **だけ**をサービスマンPCへ渡します。`device.key` はデバイス外へ出しません。

### 2. 作業者PCでClaimを発行する

```bash
aws sso login --profile technician-a
AWS_PROFILE=technician-a ./bin/edge claim \
  --api-url 'https://<API_ID>.execute-api.ap-northeast-1.amazonaws.com/poc/' \
  --tenant tenant-a \
  --request .private/edge-001/enrollment.json \
  --out .private/claim-edge-001.json
```

`--api-url` にはCDK出力の `RegistrationApiUrl` を使います。ネットワーク設定やCSR生成を終えてからClaimを発行し、残り時間を消費しないようにします。Claimファイルは**秘密鍵を含む**ため認証済みローカル接続等で安全にデバイスへ渡し、ログ・Git・共有フォルダへ置かないでください。

### 3. デバイスで初回登録する

```bash
./bin/edge provision --dir .private/edge-001 \
  --claim .private/claim-edge-001.json --mqtt-port 443
```

443では `x-amzn-mqtt-ca` ALPNを自動設定します。8883も利用できます。HTTPプロキシ専用の工場では、443が開いているだけで生のMQTT/TLSが通るとは限りません。この実装はMQTTのHTTP CONNECTプロキシを実装していません。

登録成功後はClaimファイルのすべてのコピーを安全に削除します。本番の秘密鍵・証明書・`config.json` は保持します。

### 4. 認証情報を確認して送信する

```bash
./bin/edge credentials --dir .private/edge-001

./bin/edge upload --dir .private/edge-001 \
  --file ./sample.bin --key 2026/10/sample.bin \
  --state .private/sample-upload.json --part-mib 64 --parallel 4
```

デバイス用コマンドは環境変数や作業者のAWSキーへフォールバックしません。専用のIoT資格情報プロバイダーと期限前更新を使います。

同じファイル・パートサイズ・保存先・`--state` で再実行すると、S3のパート一覧とチェックサムを照合して再開します。プロセス強制終了後の `.lock` は、プロセス停止を確認してから手動削除します。完了済みの記録は再送しません。別ファイルには別のstateファイルを使ってください。

### 5. テナント分離を検証する

```bash
# 最初に自分のパスへのuploadが成功することを確認する
./bin/edge probe-denied --dir .private/edge-001 \
  --target-tenant tenant-b --target-thing edge-003
./bin/edge probe-denied --dir .private/edge-001 \
  --target-tenant tenant-a --target-thing edge-002
```

`AccessDenied` を成功条件にします。単なる通信失敗は成功扱いにしません。予期せず許可された場合はランダム名の小さな検証オブジェクトが作成されるため、表示されたキーを作業者権限で確認・削除してください。

## セキュリティ・運用上の境界

- 登録先はIAM認証されたAPIパスで許可し、登録前フックが台帳のテナント・Thing名でパラメーターを上書きします。CSR公開鍵とClaimの対応も照合します。
- 同じClaimで別証明書を登録できないよう、台帳の2レコードを条件付きトランザクションで固定します。既存Thingの自動付け替えは拒否します。
- デバイスに許可するS3操作は、自分のプレフィックスのPutObject・Multipart中断・パート一覧だけです。読み取り・一覧・削除・IoT管理権限はありません。
- ローカル秘密ファイルはUnixで0600、新規ディレクトリは0700。Windowsでは別途NTFS ACLを設定してください。TPMによる鍵保護は未実装です。
- アップロード中のファイル変更は拒否します。最終Completeの応答を失った場合は成功／失敗が不明になるため、勝手に新規転送を開始せず作業者がS3を照合します。
- スタック削除だけでは、登録後に動的に作成されたThing・証明書は削除されません。S3・台帳・Thing Type・IoT Policy・ログは保護のため保持します。

詳細: [要件と設計](docs/design.md) / [受入試験](docs/acceptance.md) / [障害・削除手順](docs/runbook.md)

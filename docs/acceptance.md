# 受入試験

単体テストやCDK synthの成功を、AWS上の認可試験の成功と混同しない。
テスト専用アカウントとデータを使用する。管理者・tenant-a作業者・tenant-b作業者・デバイスの認証情報を分離する。

## オフライン/CI

`go test -race ./...`、`go vet ./...`、Linux amd64/arm64とWindows amd64のクロスビルド、`npm run build`、`npm test`、`npm run synth` を実行する。
Goの鍵生成・パス拒否・Credentialヘッダー/キャッシュ・Multipart再開/改変拒否と、登録前フックの純粋な認可判定・CDK権限設定を検証する。AWS SDK接続部分はモックであり、実際の認可・TLS・Fleet Provisioningの証明ではない。

## 実AWS（利用者による実行が必要）

| No. | 操作 | 期待結果 |
|---|---|---|
| 1 | tenant-a権限の作業者でtenant-aへClaim要求 | 201。台帳に同一公開鍵・顧客・Thingを保存 |
| 2 | 同じ作業者でtenant-bへ要求 | API Gatewayが403。Lambdaで別顧客登録できない |
| 3 | 署名なしで登録APIを呼ぶ | 403 |
| 4 | Claimを使って8883でCSR・RegisterThing | ACTIVE証明書、正しいThingTypeName、排他的関連付け |
| 5 | 別の新規デバイスを443/ALPNで登録 | 4と同じ。MQTTを通せるネットワークで実施 |
| 6 | 改変したCSR/別公開鍵の証明書をClaimで登録 | フック拒否。Thingなし |
| 7 | 同一Claimで2つ目の本番証明書を登録 | フック拒否。同一証明書の再送は許可 |
| 8 | 期限切れClaim、未知Claim、他テンプレート | 拒否 |
| 9 | 自分のThing名でCredentials Provider | 期限15分の一時認証情報。CLIは秘密を表示しない |
| 10 | 設定コピーで別Thing名に変更 | Credentials Providerが403 |
| 11 | 自分のS3パスへMultipart | 成功。作業者によるダウンロードのSHA-256が一致 |
| 12 | probe-deniedで別顧客・同一顧客の別デバイス | AccessDenied。先に11が成功すること |
| 13 | デバイス資格情報でGetObject/DeleteObject/IoT管理API | AccessDenied |
| 14 | 大きなファイルを送信中に通信停止、同じstateで再開 | 完了済みパートを再利用、SHA-256一致 |
| 15 | 15分を超える転送 | 資格情報更新後も完了し、同じIAM制限を維持 |
| 16 | 途中で元ファイルまたはstateの保存先を変更 | 完了・再開を拒否 |
| 17 | 本番証明書をINACTIVEへ変更 | 新規資格情報発行を拒否。既発行STSは即失効とはみなさない |
| 18 | デバイス環境に管理者AWSキーを入れ、証明書を無効化 | 管理者キーへフォールバックせず失敗 |

各ケースは実行日時、リージョン、コミットSHA、テナント、Thing、結果、AWS request IDを記録する。証明書秘密鍵、Claimファイル、STSキー/トークンは証跡に含めない。
6～8、10、13は専用テストハーネスまたはAWS APIツールで実行する手動試験であり、現行CLIが自動実行する試験ではない。

## 現時点の区別

ローカルで実行できた範囲はリリース説明/PRに記録する。GitHub ActionsはAWS credentialsを持たず、deployしない。
AWSのエンドツーエンド試験は未実施のまま「合格」として扱わない。

# 障害対応・終了手順

## 工場ネットワーク

MQTT/TLS: AWS IoT Data-ATS endpointの8883、または443 + x-amzn-mqtt-ca。
資格情報: CredentialProvider endpointのHTTPS/mTLS 443。
データ: リージョン内S3 endpointのHTTPS 443。
時刻同期と信頼されたCAストアが必要。HTTPS_PROXYはHTTPクライアントに適用されるが、MQTTのHTTP CONNECT対応はない。TLSインスペクション経路でmTLSが成立するとは限らない。TLS検証を無効化して回避しない。

## Claimが失効・応答を紛失した

DEVICEレコードを確認する。RESERVING/ISSUEDで証明書が未束縛なら、期限と60秒の猶予を過ぎた後、同じテナント・Thing名・公開鍵で再要求できる。Claim秘密鍵はバックエンドへ保存しないため再取得できない。
APPROVEDの場合は自動解除しない。IoT Registry・証明書状態・デバイスのpending.jsonを照合して管理者が復旧する。フック許可の後に登録本体が失敗する場合もある。既存Thingの無断再割り当ては行わない。

## Multipart再開

同じファイル・key・part-mib・stateで再実行する。Ctrl+Cではcontextをキャンセルして終了する。SIGKILL等で残ったstate.lockは、他のプロセスが使用していないことを確認してから削除する。
COMPLETING中に応答を失った場合、S3のNoSuchUploadは「完了済み」と「中断/期限削除済み」を区別できない。読み取り権限を持つ作業者が、オブジェクトサイズ・metadata.source-sha256・必要ならダウンロードSHA-256を照合するまでstateを削除しない。自動的な再送・上書きはしない。
未完了パートは1日後のライフサイクル削除対象になる。実際の削除は非同期。長期間オフライン後はアップロードIDが失効し得る。

## 失効

管理者が対象証明書をINACTIVE/REVOKEDに変更すると、新しいCredentials Provider取得を止められる。すでに発行された一時認証情報の即時失効とは別。既定の最大有効時間は15分であり、即時停止が要件なら対象デバイスの明示的Deny等の運用を追加し、実AWSで試験する。

## 検証終了

1. 登録開始を停止し、対象Thing・証明書・S3キー・台帳を明示的に一覧確認する。
2. 必要なデータと証跡を退避し、検証デバイスの証明書を無効化する。
3. 作業者ロールからこのスタックのManagedPolicyを外す。
4. 明示的に `cd infra && npx cdk destroy` を実行する。自動実行はしない。
5. 保持されたS3・DynamoDB・Thing Type・IoT Policy・ログと、動的に登録したThing・証明書を必要性を確認して個別に整理する。保有データ・ログには課金が続く。

Thing Typeの削除には関連するThingの解除、廃止と待機等の条件がある。スタック内のリソースだけで完結する完全自動クリーンアップは提供しない。他プロジェクトのリソースをワイルドカードで削除しない。

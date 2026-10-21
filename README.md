# aws-iot-multitenant-edge-poc

工場内エッジデバイスから AWS S3 へ、テナントを分離して直接アップロードする検証用プロジェクト。

## 採用する構成

- デバイスアプリケーション: Go
- インフラ: AWS CDK v2 / TypeScript
- 初回登録: AWS IoT Fleet Provisioning by Trusted User（デバイスで生成した秘密鍵・CSRを使用）
- テナント分離: ThingTypeName = tenantId、証明書とThingの排他的関連付け
- 認証情報: IoT Credentials Provider / 共通 Role Alias
- データ転送: AWS SDK による S3 Multipart Upload

実装・テスト・導入手順は作業ブランチで追加します。AWS へのデプロイは行っていません。

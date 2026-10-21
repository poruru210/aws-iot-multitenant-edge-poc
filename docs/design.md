# 要件・設計判断

## 合意済み

デバイスはGo、インフラはCDK v2 TypeScript。1テナントに複数デバイスを登録し、ThingTypeNameをtenantIdに使う。初期登録はTrusted User方式のFleet ProvisioningとCSR、本番認証はIoT Credentials Provider、S3へ直接Multipart Uploadする。

## この検証版で置いた仮定

リージョンは東京。UIはCLI。サービスマンはAWS IAM Identity Center等の既存ログインを利用し、テナント別API実行権限を付与される。既存収集システムは変更せず、生成済みの不変ファイルを入力とする。デバイスはLinux amd64/arm64、Windows amd64をビルド対象とする。

バックエンドはTypeScript Lambda。VPC、NAT Gateway、Private CA、Cognito、常時MQTT接続、独自STS発行API、署名URL管理APIは作らない。保存容量・転送量の実要件は未確定のため、データ自動削除は既定で無効。PoCで保存期間を指定する場合のみCDKコンテキストを変更する。

## 信頼境界

1. サービスマンのIAMポリシーが `/tenants/{tenantId}/claims` を制限する。Lambdaに任意のtenantIdを本文で渡しても使われない。作業者へ `iot:CreateProvisioningClaim` やLambda直接Invoke権限を渡さない。
2. 登録要求の公開鍵SPKI SHA-256を台帳に記録する。秘密鍵はデバイスから出さない。Claimに含まれる別の短期秘密鍵は保護して配送する。
3. 登録前フックはAWSが渡したClaim証明書IDで照合し、本番証明書のSPKIを検査する。tenant/thingは台帳から上書きする。
4. CLAIMとDEVICEのレコードを条件付きトランザクションで同じ証明書へ束縛する。同一証明書の再送だけを許可する。
5. Thingと証明書はEXCLUSIVE_THING。IoT Policyは共通Role Aliasの利用だけを許可し、S3用IAMロールはThingTypeName/ThingNameでパスを制限する。
6. S3クライアントはIoT証明書専用CredentialsCacheを使う。環境の管理者キーやカスタムS3エンドポイントは読み込まない。

台帳のAPPROVEDは「フックが許可した」という意味であり、Fleet Provisioning完了を意味しない。登録後の実状態はIoT Registry、デバイス側config、Credentials Provider・S3の疎通で確認する。

## 権限とデータ

登録ロールはAWS公式のAWSIoTThingsRegistration管理ポリシーを採用する。これは制御プレーンで広い操作を許可するため、信頼ポリシーを対象テンプレートとアカウントに固定する。デバイスには付与しない。本番化時には利用APIとログに基づき登録ロールも最小化する。

Claim秘密鍵とSTS秘密情報は台帳に保存しない。Claim履歴のみTTL7日、DEVICEレコードにはTTLを付けない。証明書とテナント所属を安易に再利用しないためである。ログはメタデータだけを14日保存し、APIレスポンス本文のログは無効化する。

S3へのアップロードはSHA-256をパートごとに指定し、再開時もListPartsで照合する。全体のローカルSHA-256をmetadataに記録し、送信前後にファイルの不変性を検査する。CompleteのETagをファイルのMD5として扱わない。

## 今回の対象外

人向けWeb画面、既存マシンの収集アダプター、自動フォルダ監視・常駐サービス、TPM、証明書ローテーション、テナント移管、MQTT用HTTP CONNECTプロキシ、自動クリーンアップ、実AWSへの自動デプロイ。これらは未実装であり、既存の構成だけで対応済みとは扱わない。

## 参照したAWS公式仕様

- Trusted User: https://docs.aws.amazon.com/iot/latest/developerguide/provision-wo-cert.html
- MQTT CSR / RegisterThing: https://docs.aws.amazon.com/iot/latest/developerguide/fleet-provision-api.html
- 登録前フック: https://docs.aws.amazon.com/iot/latest/developerguide/pre-provisioning-hook.html
- テンプレートと排他的関連付け: https://docs.aws.amazon.com/iot/latest/developerguide/provision-template.html
- Credentials Provider: https://docs.aws.amazon.com/iot/latest/developerguide/authorizing-direct-aws.html
- SourceArn/SourceAccount: https://docs.aws.amazon.com/iot/latest/developerguide/cross-service-confused-deputy-prevention.html
- MQTT/TLSとALPN: https://docs.aws.amazon.com/iot/latest/developerguide/protocols.html
- Multipart Upload: https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html

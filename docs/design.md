# 技術要件・設計判断

## 合意済みの基盤

デバイスはGo、インフラはCDK v2 TypeScript。1テナントに複数デバイスを登録し、ThingTypeNameをtenantIdに使う。初期登録はTrusted User方式のFleet ProvisioningとCSR、本番認証はIoT Credentials Provider、S3へ直接Multipart Uploadする。

## UX要件の見直し

ユーザーの指摘を受け、通常操作をCLIだけで提供する初期仮定を撤回する。サービスマンはWeb画面から機器・登録先を確認し、鍵生成、CSR、Claim、MQTT登録を一連の処理として実行する。送信時のSTS取得・更新はGoエージェントの内部処理とし、独立した操作にしない。

新しい利用者フロー・画面分担・実装差分・UX受入条件は [Web操作フロー](ux-flow.md) に記載する。**Web UI、常駐サービス、永続送信キュー、作業者Web認証は目標仕様であり、現在は未実装。** 以下の「現行」はCLI技術検証版の実装を示す。

## 現行の技術検証版で置いた仮定

リージョンは東京。現行実装はCLI。技術検証の作業者はAWS IAM Identity Center等の既存ログインを利用し、テナント別API実行権限を付与される。既存収集システムは変更せず、生成済みの不変ファイルを入力とする。デバイスはLinux amd64/arm64、Windows amd64をビルド対象とする。

バックエンドはTypeScript Lambda。現行スタックにVPC、NAT Gateway、Private CA、Cognito、常時MQTT接続、独自STS発行API、署名URL管理APIはない。今後のWeb化では既存の業務IdPを優先し、独立PoCの候補としてCognito等を検討する。人のWeb認証をデバイスのS3認証と混同しない。

保存容量・転送量の実要件は未確定のため、データ自動削除は既定で無効。PoCで保存期間を指定する場合のみCDKコンテキストを変更する。

## 現行実装の信頼境界

1. サービスマンのIAMポリシーが `/tenants/{tenantId}/claims` を制限する。Lambdaに任意のtenantIdを本文で渡しても使われない。作業者へ `iot:CreateProvisioningClaim` やLambda直接Invoke権限を渡さない。
2. 登録要求の公開鍵SPKI SHA-256を台帳に記録する。秘密鍵はデバイスから出さない。Claimに含まれる別の短期秘密鍵は保護して配送する。
3. 登録前フックはAWSが渡したClaim証明書IDで照合し、本番証明書のSPKIを検査する。tenant/thingは台帳から上書きする。
4. CLAIMとDEVICEのレコードを条件付きトランザクションで同じ証明書へ束縛する。同一証明書の再送だけを許可する。
5. Thingと証明書はEXCLUSIVE_THING。IoT Policyは共通Role Aliasの利用だけを許可し、S3用IAMロールはThingTypeName/ThingNameでパスを制限する。
6. S3クライアントはIoT証明書専用CredentialsCacheを使う。環境の管理者キーやカスタムS3エンドポイントは読み込まない。**現行のuploadもcredentialsコマンドの事前実行を必要としない。**

台帳のAPPROVEDは「フックが許可した」という意味であり、Fleet Provisioning完了を意味しない。登録後の実状態はIoT Registry、デバイス側config、Credentials Provider・S3の疎通で確認する。

Web用APIでは、上記1のIAM認証を単にJWTへ差し替えただけでテナント制限が維持されるとは扱わない。検証済みの作業者IDに基づく顧客別認可をサーバー側で実装する。端末の短期登録セッションには公開鍵との結び付け、失効、リプレイ防止、試行回数制限を追加する。登録前フックとS3側の境界は維持する。

## 権限とデータ

現行の登録ロールはAWS公式のAWSIoTThingsRegistration管理ポリシーを採用する。これは制御プレーンで広い操作を許可するため、信頼ポリシーを対象テンプレートとアカウントに固定する。デバイスには付与しない。本番化時には利用APIとログに基づき登録ロールも最小化する。

Claim秘密鍵とSTS秘密情報は台帳に保存しない。Claim履歴のみTTL7日、DEVICEレコードにはTTLを付けない。証明書とテナント所属を安易に再利用しないためである。ログはメタデータだけを14日保存し、APIレスポンス本文のログは無効化する。

S3へのアップロードはSHA-256をパートごとに指定し、再開時もListPartsで照合する。全体のローカルSHA-256をmetadataに記録し、送信前後にファイルの不変性を検査する。CompleteのETagをファイルのMD5として扱わない。

## 次のUX実装で追加する範囲

Web登録ウィザード、作業者Web認証と顧客別認可、Claim自動配送、Go常駐サービス、既存収集処理からの送信ジョブ受付、永続キュー、進捗表示、再起動からの復旧、通常利用者に秘密ファイル操作を要求しないエラー処理。これらは新しい要件であり、現行の技術CI成功を実装完了やUX試験成功とは扱わない。

既存エッジの画面・IdP・収集処理との接続条件は未確認。既存機能を優先して統合し、デバイスからS3へのデータ直接送信は変更しない。

## 別途の範囲

既存マシンの収集アダプター、全顧客の統合監視、TPM、証明書ローテーション、テナント移管、MQTT用HTTP CONNECTプロキシ、自動クリーンアップ、実AWSへの自動デプロイ。これらは未実装であり、既存の構成だけで対応済みとは扱わない。

## 参照したAWS公式仕様

- Trusted User: https://docs.aws.amazon.com/iot/latest/developerguide/provision-wo-cert.html
- MQTT CSR / RegisterThing: https://docs.aws.amazon.com/iot/latest/developerguide/fleet-provision-api.html
- 登録前フック: https://docs.aws.amazon.com/iot/latest/developerguide/pre-provisioning-hook.html
- テンプレートと排他的関連付け: https://docs.aws.amazon.com/iot/latest/developerguide/provision-template.html
- Credentials Provider: https://docs.aws.amazon.com/iot/latest/developerguide/authorizing-direct-aws.html
- SourceArn/SourceAccount: https://docs.aws.amazon.com/iot/latest/developerguide/cross-service-confused-deputy-prevention.html
- MQTT/TLSとALPN: https://docs.aws.amazon.com/iot/latest/developerguide/protocols.html
- Multipart Upload: https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html
- CredentialsCache: https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws
- Cognito PKCE: https://docs.aws.amazon.com/cognito/latest/developerguide/using-pkce-in-authorization-code.html

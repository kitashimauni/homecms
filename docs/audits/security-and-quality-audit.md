# セキュリティ・品質監査

最終確認日: 2026-09-13

## 概要

HomeCMSの認証、記事編集、メディア管理、Git連携、Local Live Preview、および開発・デプロイ手順を確認した結果をまとめる。過去の指摘は、発見時の影響・推奨対応を履歴として残し、現在の実装状態と残余リスクを分離して記録する。

初回監査では、特に認可、SSHリモート利用時のGit認証、メディアパス検証を本番運用前の必須対応として確認した。今回の再監査では、マルチサイト境界、generator-neutralなLocal Preview、shadow workspace、ブラウザ所有権の廃止、scheduled refresh、プロセスツリー停止、CI race detectorを追加確認した。

## 現行アーキテクチャの再監査結果

### 認証・セッション

- `SESSION_SECRET`はRelease modeで必須かつ32文字以上であり、cookie storeには認証用キーと暗号化用キーを別々に導出して渡す。
- セッションcookieは`HttpOnly`、`SameSite=Lax`、HTTPS時の`Secure`を設定する。GitHub tokenは暗号化cookie内に保存されるため、サーバー側セッションストアへの移行は残余リスクとして扱う。
- `TokenValidation`は一定間隔でGitHub `/user`を再検証し、明確な401だけでセッションを失効する。403、429、5xx、timeout/DNS/TLSなどは一時障害として扱い、retry backoff後に再試行する（Issue #84）。

### Local Live Previewの信頼境界

- preview hostnameはCMSのsession middlewareより前に処理するが、wildcard DNSとHost validationは閲覧者認可ではない。外部ingressではprivate networkまたは独立したviewer authenticationを必須とし、CMS session cookieをpreview subdomainへ共有しない。
- `/__homecms_ready`、`/__homecms_metadata`、`/__homecms_invalidate`（legacy pathを含む）は外部preview hostnameから404にし、loopback上のgenerator wrapperとの制御経路に限定する。
- 未保存内容はproduction working tree/Git indexではなく、site-scopedなshadow workspaceへ同期する。generatorへ渡す環境変数はallowlist境界で扱い、CMS OAuth/session/provider secretを継承させない。workspaceのtransition中や再構築失敗は503で扱う。一方、workspace manager自体が利用できない場合は保存済みrepoへフォールバックする実装が残っているため、ログ監視とfail-closed化を残余リスクとする。
- iframeは別originのpreview URL、`sandbox`、`referrerpolicy=no-referrer`を使用する。ただし現行テンプレートは`allow-same-origin`、`allow-modals`、`allow-popups`を許可しているため、テーマ由来JavaScriptと外部viewer認証画面の影響は残余リスクであり、CSPと許可トークンの最小化を今後の課題とする。

### 並行性・マルチサイト・プロセス終了

- APIは`?site=`または`X-CMS-Site`で対象siteを明示し、記事、preview、deployment、workspace、runtime stateをsite単位で分離する（Issue #83/#85/#86）。ブラウザtabをruntimeの所有者とは扱わず、同一siteの複数tabは同じshadow workspaceへ収束させる。
- article switch、Git Sync、production save、Local Preview updateは、古いrequestの結果をcommitしない世代管理とin-flight待機を共通化する。scheduled refreshはworkspaceをdetach/deleteせずgenerator processだけを再起動する。
- Unix系では独立process groupを、WindowsではJob Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）を使い、wrapper配下のgeneratorもStop、idle cleanup、Git Sync reset、shutdownから同じ停止契約で終了させる。WindowsではJob割当まで子生成を抑止するためプロセスを一時停止して起動する。

### 品質・運用

- Frontend testはUI state、Local Preview、editor state/workflow、site-scoped APIに分割し、fake browser/storage harnessを共有する。Go側もLocal Previewのrepo/runtime/workspace/process fixtureを共有し、generator-neutralな重複ケースを増やさず、Hugo/Eleventy固有のfixture・integration testは維持する。global config mutationはlegacy site registryを検証するcontract testに限定し、各テストで復元する（Issue #78）。テスト層の責務は[テスト構成ガイド](../guides/testing.md)に整理した。
- CIは通常の`go test`、`go vet`、build、JavaScript testに加えて、Linux上の独立した`go test -race ./...` jobを実行する（Issue #87）。
- Dockerはloopback公開、非root runtime、`HOMECMS_REPOS`明示allowlist、secret-freeな`tool-bootstrap`を維持する。

### 残余リスクと今後の作業

- Cookie storeからサーバー側session ID方式への移行、preview iframeのsandbox/CSP許可範囲縮小、実際のwildcard ingress + 外部viewer authenticationを使った受入確認は未完了。
- race detectorはLinux CIで実行するため、Windows固有のJob Object実行時挙動はWindows CIまたは実機で追加確認する。現時点ではWindows専用ビルドとJob Objectテストを用意している。
- 本文・設定・外部サービスに依存するgenerator固有の挙動は、Hugo/Eleventy双方の実fixtureを更新した際に再検証する。

## 優先度: 重大

### 1. 許可リスト未設定時にすべてのGitHubユーザーを許可する

- 状態: 対応済み
- 該当箇所:
  - `pkg/config/config.go` の `AllowedGitHubUsers`
  - `pkg/config/config.go` の `IsUserAllowed`
  - READMEのクイックスタート
- 影響:
  - `ALLOWED_GITHUB_USERS` が空の場合、GitHubアカウントを持つすべてのユーザーがCMSへログインできる。
  - ログイン後は記事の保存・削除、メディア操作、同期、公開、Hugo再起動などを実行できる。
  - 現在のローカル `.env` にも `ALLOWED_GITHUB_USERS` は設定されていない。
- 推奨対応:
  - 本番環境では許可リストを必須にし、空の場合は起動を失敗させる。
  - 開発時だけ明示的な設定で全員許可を選べるようにする。
  - READMEと`.env.example`の初期設定を安全側へ変更する。
  - 各リクエストでも保存済みユーザー名を再認可し、許可リスト変更後に既存セッションを失効できるようにする。

### 2. SSHリモートではログインユーザーのOAuth権限で公開されない

- 状態: 対応済み
- 該当箇所: `pkg/services/git.go` の `ExecuteGitWithToken`
- 影響:
  - OAuthトークンを使う処理はHTTPSリモートを前提としている。
  - SSHまたはscp形式のリモートでは、Gitはサーバー側のSSH鍵・SSH設定を使用する。この場合、ログインユーザー本人に対象リポジトリの書き込み権限がなくても、サーバーの鍵に権限があれば公開できる。
  - 現在の`repo`のリモートも `github:kitashimauni/techblog.git` というSSH形式である。
  - `GIT_REMOTE`を変更しても、リモートURL取得処理が`origin`に固定されている。
- 推奨対応:
  - ユーザーのOAuth権限で公開する場合はHTTPSリモートのみを許可し、起動時に検証する。
  - SSH鍵で公開する設計にする場合は、その事実を明示し、OAuthログインとは別に厳格なCMS認可を行う。
  - リモート名のハードコードを廃止し、すべて`config.GitRemote`を使用する。
  - 実装では`github.com`のHTTPSリモートだけを許可し、credential helperを無効化してログインユーザーのOAuthトークンを使用する。
  - 既存のSSH形式リモートはHTTPS形式へ移行する必要がある。

### 3. メディアアップロード先がリポジトリ外へ脱出できる

- 状態: 対応済み
- 該当箇所:
  - `pkg/handlers/media.go`
  - `pkg/services/media.go` の `ListMediaFiles`
  - `pkg/services/media.go` の `SaveMediaFile`
- 影響:
  - contentモードの`articlePath`が検証されていない。
  - `../../`を含むパスにより、許可された拡張子のファイルをリポジトリ外の任意ディレクトリへ書き込める。
  - メディア一覧でもリポジトリ外のディレクトリを走査できる。
- 推奨対応:
  - `articlePath`を必ず`content`配下へ安全に解決する。
  - 設定由来の`ARTICLE_MEDIA_DIR`と`STATIC_MEDIA_DIR`も絶対パスや親ディレクトリ参照を拒否する。
  - シンボリックリンクを含め、実パスが許可ルート内にあることを確認する。
  - 正常系、`../`、絶対パス、Windows形式パス、シンボリックリンクのテストを追加する。

### 4. GitHubアクセストークンをCookieへ保存している

- 状態: 暗号化Cookieで対応済み。サーバー側セッションストアへの移行は残余リスク
- 該当箇所:
  - `main.go` のCookie Store初期化
  - `pkg/handlers/auth.go` の`access_token`保存
- 当時の影響:
  - 以前はCookie Storeへ認証鍵だけを渡していたため、Cookieが署名のみで、Cookie漏えい時にGitHub OAuth tokenが露出する可能性があった。
- 現在の緩和:
  - `main.go`はsecretから認証用SHA-512キーと暗号化用SHA-256キーを独立導出し、`cookie.NewStore(authKey[:], encryptionKey[:])`へ渡す。
- 推奨対応:
  - サーバー側セッションストアへ移行し、CookieにはランダムなセッションIDだけを保存する。
  - Cookie Storeを継続する場合は、署名鍵と独立したAES暗号化鍵を設定する。
  - `Secure`、`HttpOnly`、`SameSite`設定を維持し、鍵のローテーション方法も定義する。

## 優先度: 高

### 5. 自動保存完了前に公開できる

- 状態: 対応済み
- 該当箇所:
  - `static/js/editor.js` の3秒後の自動保存
  - `static/js/app.js` の`publishFile`および`runPublish`
- 影響:
  - 編集直後に公開すると、ディスクへ保存される前の内容がコミットされる。
  - 公開成功表示の後に自動保存が動き、最新内容が未公開の変更として残る。
- 推奨対応:
  - 公開前に保留中のタイマーを停止し、最新内容の保存完了を待つ。
  - 保存失敗時は公開を中止する。
  - 公開ボタンの多重実行と、自動保存の同時実行を直列化する。

### 6. `git commit`失敗後もpushし、成功扱いになる

- 状態: 対応済み
- 該当箇所: `pkg/services/git.go` の `PublishChanges`
- 影響:
  - コミット失敗が警告文字列へ変換されるだけで、処理はpushへ進む。
  - pushが成功すればAPIは成功を返すため、変更がコミットされていないのに「公開成功」と表示される。
- 推奨対応:
  - 「変更なし」以外のコミット失敗では直ちに処理を中止する。
  - 「変更なし」の場合も、対象変更が本当に存在しないことを確認して明示的な結果を返す。
  - 同期・保存・公開を排他制御し、Git indexと作業ツリーの競合を防ぐ。

### 7. JSON Front Matterで本文が失われる

- 状態: 対応済み
- 該当箇所: `pkg/services/frontmatter.go`
- 影響:
  - JSON Front Matterの読み込みはファイル全体をJSONとして解析するため、本文付きの記事を解析できない。
  - JSON形式の書き出しでは本文が出力されない。
  - 対象記事の保存時に本文を失う可能性がある。
- 推奨対応:
  - Hugoが扱うJSON Front Matterと本文の境界を正しく解析・生成する。
  - YAML、TOML、JSONすべてについて、読み込み後の保存で本文が保持されるラウンドトリップテストを追加する。

### 8. Hugo/Eleventyプレビューと管理画面の信頼境界

- 状態: preview ingressとCMS管理画面を別originへ分離済み。iframeの許可範囲は残余リスク
- 該当箇所:
  - `main.go` のHugoリバースプロキシ
  - `templates/index.html` のプレビューiframe
- 影響:
  - preview側のテーマJavaScriptはCMS管理画面と別originで実行されるが、preview ingressのviewer authenticationが未設定ならURLの知識だけで閲覧できる。
  - 現行iframeは`sandbox`を付けているものの、generator互換性のため`allow-same-origin`、`allow-modals`、`allow-popups`を許可している。
- 現在の緩和:
  - `LocalPreviewIngress`はCMS session middlewareより前にpreview hostnameを処理し、CMS cookieをpreview subdomainへ共有しない。
  - wildcard DNS/Host validationを認可とみなさず、external ingressへprivate networkまたはCloudflare Access等の独立viewer authenticationを要求する。
  - control pathは外部hostnameから遮断し、loopback wrapperとの内部通信に限定する。
- 残余リスク/推奨対応:
  - iframe sandbox/CSPの許可範囲を実サイトで検証し、必要最小限まで縮小する。viewer authenticationを必ずpreview ingressへ設定する。

## 優先度: 中

### 9. Hugo再起動時にプロセス管理が競合する

- 状態: 対応済み
- 該当箇所:
  - `pkg/services/process_manager.go`
  - `pkg/services/hugo_adapter.go`
- 影響:
  - 旧プロセスの監視goroutineが、新プロセス起動後に共有変数を`nil`へ戻す可能性がある。
  - Hugoの二重起動、稼働状態の誤判定、停止不能につながる。
- 推奨対応:
  - 監視goroutineが、自分の監視対象と現在のプロセスが同一の場合だけ共有状態をクリアする。
  - 固定時間の`sleep`ではなく、プロセス終了を明示的に待つ。
  - 起動、異常終了、連続再起動のテストを追加する。

### 10. `CACHE_CONCURRENCY`の値によって停止またはpanicする

- 状態: 対応済み
- 該当箇所:
  - `pkg/config/config.go`
  - `pkg/services/cache.go`
- 影響:
  - `CACHE_CONCURRENCY=0`では記事一覧のキャッシュ構築がデッドロックする。
  - 負数ではチャネル作成時にpanicする。
- 推奨対応:
  - 設定読み込み時に1以上の上限付き整数へ制限する。
  - 不正値では安全な既定値を使用するか、起動を失敗させる。

### 11. コレクションのFront Matter形式指定を無視する

- 状態: 対応済み
- 該当箇所:
  - `pkg/models/cms.go`
  - `pkg/services/frontmatter.go` の `GenerateContentFromCollection`
- 影響:
  - コレクションモデルに`format`がなく、新規記事は常にTOMLで生成される。
  - YAMLまたはJSONを指定したCMS設定と一致しない。
- 推奨対応:
  - CMS設定モデルに`format`を追加する。
  - `toml-frontmatter`、`yaml-frontmatter`、`json-frontmatter`などの値を内部形式へ正規化する。
  - 未対応形式は暗黙にTOMLへ変換せず、設定エラーとして通知する。

### 12. Goバージョンとデプロイ文書が一致していない

- 状態: 対応済み
- 該当箇所:
  - `go.mod`: Go 1.24.0、toolchain Go 1.24.11
  - README: miseでGo 1.24.11を使用
  - `docs/guides/deployment.md`: `golang:1.24-alpine`
- 影響:
  - 記載されたDockerfileでは現在の`go.mod`をビルドできない。
  - 開発者ごとに異なるGoバージョンが使われる可能性がある。
- 対応内容:
  - `mise.toml`のGo 1.24.11を開発標準としてREADMEへ明記した。
  - Dockerfile例をGo 1.24系へ更新した。
  - 引き続きCIを追加する場合は`mise run check`またはGo 1.24系で統一する。

## その他の改善点

- 記事取得APIのパスをフロントエンド側でURLエンコードしていないため、`&`、`#`、`?`などを含むファイル名を正しく扱えない。（対応済み）
- Git statusのporcelain出力を文字列操作で解析しており、リネームや引用された日本語ファイル名のdirty判定を誤る可能性がある。（`--porcelain=v1 -z`の解析へ変更済み）
- 同一siteの複数tabでの更新はsite-scoped shadow workspaceへlast-write-winsで収束する。厳密な編集者単位の競合解決は将来改善とする。
- Windows固有のJob Object動作はWindows CI/実機で追加検証する。

## 検証結果

以下は成功した。

- `go test ./...`
- `go vet ./...`
- `go build -buildvcs=false .`
- JavaScriptファイルの構文検査
- Linux CIで`go test -race ./...`

テストカバレッジ:

- `pkg/handlers`: 19.8%
- `pkg/services`: 11.8%
- `main`、`pkg/config`: 0%

ローカルで完走できなかった検査:

- `go test -race ./...`
  - Windows環境ではrace detectorの実行条件を満たさないため、Linux CIのrace jobで実行する。
- `staticcheck ./...`
  - インストール済みのstaticcheckがGo 1.24の解析中にpanicした。

重要な異常系の大半が未テストであるため、修正時は各問題の再現テストを先に追加することを推奨する。

## mise導入方針

### 結論

このプロジェクトでは開発環境とサイト別toolchainにmiseを採用している。ただし両者の設定と信頼境界は分離する。

CMS rootの`mise.toml`はCMSの開発・テスト用Go/Node.jsを固定する。Hugo、サイト生成用Node.js、package managerは各サイトrepoのmise設定で固定し、CMS全体へ単一バージョンを強制しない。

### miseで管理する対象

- CMS開発用Go 1.24.11とNode.js 22
- サイト別Hugo/Node.js/package manager
  - 実サイトの要件を確認し、各サイトrepoで固定する。
- staticcheck
  - Go 1.24対応版へ更新して固定する。
- 必要に応じてgolangci-lint

### miseタスクとして揃えるコマンド

- `test`: `go test ./...`
- `test-race`: LinuxまたはCコンパイラを用意した環境で`go test -race ./...`
- `vet`: `go vet ./...`
- `lint`: `staticcheck ./...`、またはgolangci-lint
- `fmt-check`: `gofmt`未適用ファイルがないことを確認
- `build`: `go build .`
- `check`: format、vet、lint、test、buildをまとめて実行

### 導入時の注意

- `go.mod`の`go`・`toolchain`指定は残し、Go側でも最低要件を検証する。
- miseと`go.mod`、Docker、CIのバージョンを同時に更新し、二重管理のずれを防ぐ。
- OAuth Client Secretやセッション鍵などの秘密情報を`mise.toml`へ直接書かない。
- `.env`は引き続きGit管理外とし、本番ではサービス管理基盤のSecret機能を使用する。
- HugoはExtended版が必要かをサイト側で確認し、通常版と混在させない。
- race detectorはmiseだけでは解決しないため、Linux CIで実行するのが扱いやすい。
- Dockerではapp起動時にrepo設定を自動trustしない。`HOMECMS_REPOS`の明示allowlistとsecret-freeな`tool-bootstrap` one-shotを使用する。
- Node.js依存はレビュー済みlockfileからfrozen installし、HTTP request処理中やapp起動時には取得しない。

### 推奨する導入順序

1. CMS rootの`mise.toml`で開発用Go/Node.jsを、各サイトrepoで生成用toolchainを固定する。
2. READMEとデプロイ文書をmise前提の手順へ更新する。
3. `mise run test`、`mise run vet`、`mise run build`を定義する。
4. Go 1.24対応のlintツールを固定する。
5. CIでもmiseを使用するか、少なくとも同じバージョンとタスクを再現する。
6. DockerのbuilderイメージをGo 1.24系へ更新する。

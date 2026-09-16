# 本文プレビューとデプロイプレビュー

## ステータス

現在のHomeCMSは、CMS内の安全な「本文プレビュー」、site別hostnameで設定済みgenerator（Hugo/Eleventyなど）を動かす「Local Live Preview」、外部providerが生成する「デプロイプレビュー」の3段階を持つ。

Issue #30で旧`/admin/preview/:site/*` path-prefix proxyとローカルgenerator preview processを廃止したが、Issue #32ではpath prefixを復活させず、`https://<site-id>.<preview-domain>/`をorigin rootとする方式でLocal Live Previewを再導入する。

Phase 1で設定model、derived URL、Host validation、process lifecycle/port reservation契約を導入し、Phase 2でHugo preview processのlazy start/stop、loopback port retry、hostname reverse proxy、redirect補正、WebSocket/LiveReload中継を実装する。未保存editor stateのshadow workspace連携はPhase 3で実装する。TLS終端、wildcard DNS、DNS-01、Tailscale等のpreview ingressはCMS本体から分離する。

## 現行の決定

本文プレビューは編集中のMarkdownを即時に確認する機能であり、サイトプレビューではない。GoldmarkのGFM拡張で見出し、段落、リスト、引用、コードブロック、表、リンク、画像をHTML化し、Bluemondayでsanitizeする。raw HTML、`javascript:` URL、event handlerは許可しない。既存のローカル画像は、認証済みの`/admin/api/media/raw`へ書き換える。Hugo shortcode、generator固有template、サイトCSS/JavaScript、taxonomy、menu、外部dataは再現しない。

デプロイプレビューは次の境界を持つ。

```text
Editor
  -> save working tree
  -> explicit deployment update
  -> temporary Git index
  -> cms-preview/<draft-id> commit and push
  -> PreviewDeploymentProvider
  -> exact commit deployment status and immutable URL
```

一時Git indexを使うため、CMSはproduction branchのworktreeをcheckoutしない。draftごとにbranchとstateを分離し、同じrepositoryに対するGit writeはrepository lockで直列化する。autosaveやキー入力ではremote buildを起動せず、「デプロイプレビューを更新」の明示操作だけがcommit/pushを行う。

## Local Live Previewとの役割分担

previewは次の3段階として扱う。

1. **Markdown本文プレビュー**
   - generatorを実行しない
   - 編集入力を即時に安全表示する
   - theme/layout/shortcodeの再現は目的としない
2. **Local Live Preview**（Issue #32 Phase 2でHugo runtime/proxyを実装）
   - Hugo preview processを実際に起動する
   - `<site-id>.<preview-domain>`をsite自身のorigin rootとして利用する
   - theme/layout/shortcode/CSS/JS/LiveReloadを確認する
   - remote commit/pushを要求しない
   - Phase 2では保存済みrepository contentを表示し、未保存editor入力はPhase 3でshadow workspaceへ反映する
3. **Deployment Preview**
   - 明示操作でdraft branchをcommit/pushする
   - 外部providerでbuildし、特定commitの本番同等表示を確認する
   - 公開前の最終確認を目的とする

Local Live PreviewはDeployment Previewの置き換えではなく、Markdown本文プレビューとDeployment Previewの間を補う機能とする。

## Provider契約

providerはhandler/UIから分離し、次の責務を持つ。

- `Trigger`: branch push後のdeployment追跡を開始する
- `Status`: deployment IDまたはcommit SHAから状態を取得する
- `URL`: readyかつ同一commitのimmutable URLを返す
- `Delete`: 不要なdeploymentを削除する
- `Retry`: failed deploymentを明示操作で再試行する（optional拡張）

CMSが公開する状態は`queued`、`building`、`ready`、`failed`に正規化する。ready URLはproviderが返したdeployment固有URLのみを採用し、branch aliasは採用しない。build中・失敗時に過去のready URLを新しいcommitの結果として表示してはならない。

stateにはsite ID、draft ID、正規化済みarticle path、commit対象paths、branch、commit SHA、deployment ID、status、URL、作成・更新時刻を保存する。publish時はrequestのpathから対象を再計算せず、stateをsource of truthとする。tokenはstate、ログ、APIレスポンスへ保存しない。

## Publishとcleanup

Publishはproduction branchへ直接commit/pushせず、`cms-preview/<draftID>`からproduction branchへのPull Requestを作成し、レビュー後のmergeを公開操作とする。Deployment Previewがreadyの場合は同一draft lockの下でprovider status、stateに保存したpathsとworking treeの一致、remote branch SHA、既存または作成したPRの`head.sha`を順に確認し、すべてがpreview済みcommitと一致する場合だけPR URLを返す。Deployment Previewが無効・更新中・失敗・古い状態の場合も、現在のproduction working treeから対象記事と参照mediaをdraft branchへcommitし、プレビューURLを要求せずPRを作成できる。どちらの経路もGit mutation/sync gateとremote branchの二重確認を維持する。draft破棄時はremote branchとprovider deploymentをcleanupする。cleanup失敗はstateに残し、同じ明示操作で再試行できる。

## セキュリティ

### Markdown本文プレビュー / Deployment Preview

- provider tokenは設定された環境変数名からserver側だけで読み、browserへ返さない
- Cloudflare tokenは対象Pages projectに必要な最小権限とする
- preview deploymentは既定で公開され得るため、未公開contentにはCloudflare Access等のアクセス制御を設定する
- `noindex`は認証の代替にしない
- Access付きURLはiframeへ埋めず、新しいtabで開く
- production secretやproduction DB更新権限をpreview buildへ渡さない
- UIはAccess未保護設定を明示的に警告する

### Local Live Preview

- Site Registryでallowlistされたsiteだけを起動対象にする
- preview namespaceに属するinvalid/unknown HostはCMS admin routeへfall throughさせず404相当で拒否する
- Host値から任意repository path、port、commandを生成しない
- generatorへ渡す環境変数をallowlistし、CMSのOAuth/session/provider secretを継承させない
- generator portを`127.0.0.1`だけへbindし、preview ingress経由でのみ到達させる
- wildcard DNSやHost validationを閲覧者認可として扱わない
- preview ingressはTailscale等のprivate network内に置くか、Internet reachableな場合はCloudflare Access等の独立viewer authenticationを必須とする
- preview hostname requestはCMS session middlewareより前で処理し、CMS session cookieをpreview subdomainへ共有しない
- TLS証明書やDNS provider tokenをCMS自身が保持することを必須にしない

## Local Live Preview process / proxy

generator processは最初のpreview requestでlazy startし、内部portはCMSが`14100-14999`から予約する。port利用可否をloopbackでprobeしたあとに起動し、bind race等でstartupに失敗した場合はslotを解放して別portで有限回retryする。

Hugoにはexternal preview URLを`--baseURL`として渡し、`--appendPort=false`を使う。HTTPS previewではLiveReload portを443、HTTP previewでは80に固定し、internal portをbrowserへ露出させない。`--renderToMemory`、draft/future content、watchを有効にする。

reverse proxyはpath prefixを加えずrequest pathをそのままHugoへ渡す。root-relative `/css/...`、`/js/...`、`/images/...`はsite自身のorigin rootから取得される。内部`127.0.0.1:<port>`または`localhost:<port>`を指すabsolute `Location`だけをexternal preview originへ書き換え、外部redirectは保持する。HTTP Upgradeを透過してHugo LiveReloadのWebSocketを通す。

CMS shutdown時には起動済みgenerator processを停止する。異常終了したprocessはfailed状態となり、次のrequestで再起動可能とする。

## ライフサイクルと競合

draft IDはbrowser sessionとsite/article pathの組み合わせごとに生成する。`crypto.randomUUID()`が利用できない非HTTPS環境では`crypto.getRandomValues()`からUUID v4を生成し、暗号学的乱数源がない場合はfail closedとする。同じdraftの同時更新は拒否し、複数site・複数draftのstateを混在させない。同じdraft IDを別記事へ再利用することも拒否する。

preview更新は既存preview commitではなく、その時点のlocal production branchをbaseにして対象pathsだけを一時indexへ適用する。production branchが進んでnon-fast-forward更新になる場合は、前回commitを期待値とする`--force-with-lease`でdraft branchだけを更新し、失敗時はlocal draft refをrollbackする。commit SHAでdeploymentを照合するため、providerがbranch aliasを新しいbuildへ切り替えている途中でも誤ったpreviewを表示しない。

Local Live Previewの未保存editor stateはproduction working treeやGit worktreeへ書かず、CMS管理領域の**shadow content workspace**へ保持する方針をIssue #32 Phase 1で確定した。初期実装では同一siteにつきactive Local Live Preview sessionを1つに制限し、別draft/tabの未保存stateを混在させない。workspace作成・同期・cleanupはPhase 3で実装する。詳細は[Local Live Preview設計](local-live-preview-design.md)を正とする。

## 移行

`preview_url`、`hugo_server_bind`、`hugo_server_port`はIssue #30以前のpath-prefix型ローカルpreview用の旧設定であり、現行の本文/デプロイプpreviewおよび新Local Live Previewの公開URL/port管理には使用しない。`/admin/preview/:site/*`、`POST /admin/api/build`、`POST /admin/api/build/restart`も廃止済みであり、Issue #32でもこれらを復活させない。

新Local Live Previewは`LOCAL_LIVE_PREVIEW_ENABLED`、`PREVIEW_DOMAIN`、`PREVIEW_SCHEME`とsite単位の`preview.local_preview.enabled`を使用する。`https://<site-id>.<preview-domain>/`はderived valueとして生成し、旧`preview_url`等との互換性を前提にしない。通常siteは最初のpreview requestでlazy startし、`preview.local_preview.always_on: true`のsiteだけはCMS起動後にbackground supervisorが非同期prewarmする。

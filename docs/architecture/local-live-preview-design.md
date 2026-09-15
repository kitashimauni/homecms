# Local Live Preview設計

## ステータス

Issue #32のLocal Live Previewは段階的に実装する。

- Phase 1 (#33): 設定model、derived URL、Host validation、process lifecycle/port reservation
- Phase 2 (#34): Hugo lazy start/stop、loopback port、hostname reverse proxy、redirect補正、WebSocket/LiveReload中継
- Phase 3 (#35): shadow content workspace + editor debounce + content resource同期
- Phase 4: UI/運用導線、generator別URL解決
- Issue #46: Eleventy `--serve`、project-root overlay、generator metadata URL解決
- Issue #68: Eleventyのlast-known metadataを使ったcached URL先行navigationとfresh URL収束
- Issue #37: 実blog・wildcard ingressでのHugo/Eleventy受け入れ確認は実環境で継続する

Local Live PreviewはMarkdown本文プレビューとDeployment Previewを置き換えず、両者の中間を担う。

```text
Markdown本文プレビュー
  -> generatorを実行しない安全な即時確認

Local Live Preview
  -> Hugo/theme/layout/shortcode/CSS/JSを使う編集中確認

Deployment Preview
  -> remote buildした特定commitを公開前に最終確認
```

## URL / ingress / viewer authentication

Local Live Previewはpath prefixではなくsiteごとのhostnameをorigin rootとして使う。

```text
PREVIEW_DOMAIN=preview.example.com
PREVIEW_SCHEME=https

tech  -> https://tech.preview.example.com/
daily -> https://daily.preview.example.com/
```

DNSは`*.preview.example.com`のwildcardを想定する。TLS、DNS-01、Cloudflare、Tailscale、Caddy/Traefik/Nginx等はpreview ingressの責務であり、hugo-cmsへ証明書秘密鍵やDNS provider credentialを持たせることを必須にしない。

wildcard DNSやHost validationは閲覧者認可ではない。preview ingressはTailscale等のprivate network内に置くか、Internet reachableならCloudflare Access等の独立viewer authenticationで保護する。CMS session cookieはpreview subdomainへ共有しない。

## 設定 / Host routing

```env
LOCAL_LIVE_PREVIEW_ENABLED=false
PREVIEW_DOMAIN=preview.example.com
PREVIEW_SCHEME=https
```

```yaml
sites:
  - id: tech
    repo_path: /data/repos/tech-blog
    generator: hugo
    preview:
      local_preview:
        enabled: true
        always_on: true
        refresh:
          times: ["04:00"]
          timezone: Asia/Tokyo
```

有効site IDはlowercase DNS labelで、`<site-id>.<preview-domain>`全体も253文字以内の有効DNS名でなければならない。

HTTPSではport省略または`:443`、HTTPではport省略または`:80`だけを許可する。preview namespace内のinvalid/unknown HostはCMS admin routeへfall throughさせない。HostはSite Registry lookupにだけ使い、repository path、command、internal portへ変換しない。

## Generator process lifecycle

`LocalPreviewManager`がsiteごとのgenerator processを管理する。generatorごとの差異はprocess command factoryと`PreviewURLResolver`へ閉じ込め、lifecycle、port reservation、proxy、shutdownは共通化する。

```text
stopped -> starting -> ready -> stopping -> stopped
                 \-> failed <-/
```

- preview hostnameへの最初のrequestでlazy start
- `127.0.0.1`だけへbind
- internal port rangeは`14100-14999`
- port probe後のbind raceでは有限回retry
- child environmentはgenerator allowlistのみ
- CMS shutdown開始後は新規lazy startを拒否
- HTTP server drain後にchild processを停止
- Unix/Linuxではgeneratorを独立process groupで、WindowsではJob Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）で起動し、wrapper配下もgraceful stop→grace period→forced terminationの順で停止
- process tree終了と`cmd.Wait()`を確認してからlifecycle slotをreleaseし、timeout時はPID/process groupを診断ログへ残す
- 通常siteは異常終了後に次requestで再起動可能
- `always_on: true`のsiteはCMS起動後にbackground supervisorがprewarmし、異常終了をbounded exponential backoffで再起動
- daily refreshは`HH:MM`配列とIANA timezoneで表し、workspaceを保持したままgeneratorだけを再起動

Hugoは概ね次相当で起動する。

```text
hugo server
  --source .
  --environment development
  --contentDir <content-dir>
  --bind 127.0.0.1
  --port <internal-port>
  --baseURL https://<site-id>.<preview-domain>/
  --appendPort=false
  --liveReloadPort 443
  --renderToMemory
  --buildDrafts
  --buildFuture
  --buildExpired
  --watch
  --noHTTPCache
```

Eleventyは対象siteのpackage managerを再利用し、production repositoryを基準に作ったtemporary project-root overlayをcwdにする。overlayでは既存の設定・依存関係・generator固有ディレクトリを参照し、`content_dir`と`public_dir`だけをpreview専用領域へ置き換える。

```text
<package-manager> exec node <CMS>/scripts/eleventy-local-preview.cjs
  --serve
  --input <project-relative-content-dir>
  --output <temporary-project-public-dir>
  --port <internal-port>
  --host 127.0.0.1
```

CMSのNodeラッパーはEleventyのprogrammatic `watch`で再ビルドし、Eleventyの初回build前にCMS側のHTTP/LiveReload WebSocket serverを`127.0.0.1`へbindする。初回build後はprogrammatic APIのincremental buildを有効にし、front matterが不変の既知記事の本文変更だけを変更対象と依存出力の差分処理にする。front matter変更、記事の追加・削除、layout・data・configなど記事metadata mapに紐づかない変更ではfull buildを行い、collection依存ページを含む出力を再生成する。`/__homecms_ready`は初回build中に503、build完了後に200を返す。Eleventy標準Dev Serverのhost省略時のbind挙動や`HOST`環境変数には依存しない。出力ディレクトリはproductionの`public`/`_site`を上書きせず、停止時にtemporary outputを削除する。LiveReload通知にはbuild generationを含め、埋め込みiframeが同じbuildを再読込済みであることをCMSへ伝える。

記事選択時の初回起動では、shadow workspaceを含むtemporary project-root overlayをgeneratorの入力として使う。CMSはgeneratorに依存しないURL解決契約を介して解決する。Hugo実装はserverと同じ`--environment development`を指定し、`HUGO_CONTENTDIR`と`HUGO_BASEURL`のenvironment variableでshadow contentとLocal Preview URLをoverrideし、`--noBuildLock`を渡す。Eleventy実装は稼働中wrapperが`eleventy.after`の結果から作る`inputPath -> url` mapを`/__homecms_metadata`で公開し、CMSは同じprocessへ問い合わせる。body-only incremental build時の結果は変更分だけなので、wrapperは既存mapへ差分をマージし、削除されたinvalidated pathを除去する。front matterや広い依存の変更時はfull buildを実行してmap全体を更新する。workspace updateはshadow fileの書き換え直前にmetadataをinvalidateする。既知の記事はwatch build generation中もlast-known mapを`stale`として返し、未知の記事や初回build前の記事だけはfresh mapまで待つ。記事切替はcached URLを先に表示し、build後にfresh URLが変わった場合は再遷移し、同じURLでasset復旧が必要な場合は一度reloadする。watch rebuild完了ごとにmapを置き換えるため、通常経路でresolver専用のJSON full buildやpreview outputの共有・resetは行わない。取得したURLはpath、query、fragmentを保持してLocal Preview originへ変換し、CMSはpermalink、slug、Data Cascade、paginationを再実装しない。以降の同一記事の編集はgeneratorのwatch/live reloadを利用する。

## Persistent preview supervisor

site registryの`preview.local_preview.always_on`が`true`で、かつ`enabled`も有効なsiteだけを対象にする。CMSのHTTP server startupはprewarm完了を待たず、supervisorはbackgroundで次を担当する。

- startup prewarm
- process exitの検知と500ms開始・最大30秒のbounded exponential backoff restart
- `refresh.times`のdaily generator restart
- CMS shutdown contextによるsupervisorとprocessの停止

`enabled: false`は`always_on`より優先される。`refresh.timezone`はIANA timezoneで、省略時はUTCとする。次回refresh時刻と実効timezoneは`GET /admin/api/preview/local/status`へ返す。

scheduled refreshは`ResetRuntime`を使わない。site gateを保持して現在のruntimeを取得し、次の順でprocessだけを再起動する。

```text
current runtime
  ├─ active shadow workspaceあり -> shadow workspaceを維持
  └─ shadow workspaceなし       -> production contentを使用
scheduled refresh
  -> generator processをgraceful stop
  -> workspaceはdetach/deleteしない
  -> 同じcontent sourceでgeneratorを起動
  -> ready確認
```

always-on siteはidle reaperの対象外とする。明示Stopとeditor update時のproduction-to-shadow切替は手動停止として扱い、次のpreview requestまでsupervisorが復活させない。Git Sync reset成功後は手動停止を解除し、同期後のproduction contentを使って再度prewarmする。shutdown contextのキャンセルは新しいrestartを中止し、generator process treeとsupervisor goroutineを残さない。

## Reverse proxy / LiveReload

preview requestはpathを書き換えずHugoへproxyする。

```text
https://tech.preview.example.com/css/main.css
  -> http://127.0.0.1:<internal-port>/css/main.css
```

external Hostを保持し、`X-Forwarded-Host` / `X-Forwarded-Proto`を再構成する。内部loopbackを指すabsolute `Location`だけをexternal preview originへ書き換え、HTTP Upgradeを透過してLiveReload WebSocketを通す。

## Phase 3: shadow content workspace

### 目的

editorの約250ms debounce更新をproduction working treeへ直接書かず、CMS管理の一時content directoryへ反映する。

```text
Editor input
  -> 250ms debounce
  -> POST /admin/api/preview/local
  -> shadow content directory
  -> generator watcher
  -> rebuild / LiveReload
```

既存の3秒autosaveは保存機能として残るが、Local Live Previewのupdate経路自体はGit working tree/index/refへ書かない。

### workspace

初回update時にsiteの`content_dir`全体をOS temporary directoryへmirrorする。

```text
OS temporary directory/
  <site-id>/
    package.json, node_modules, eleventy.config.js ... production references
    src/ or content/ ... shadow content_dir
    _includes/, _data/, _layouts/ ... production references
    public/ ... empty preview output directory
```

Hugoは従来どおり元repositoryをsource rootとして読み、`--contentDir`だけをshadow directoryのabsolute pathへ差し替える。Eleventyはtemporary project-root overlayをcwdにして`--input <content_dir>`、`--output <temporary-project-public-dir>`で実行する。overlayでは`content_dir`をshadowへmaterializeし、`public_dir`を空のpreview専用directoryにする。それ以外のroot-relativeなconfig、collection glob、includes/layouts/data、passthrough asset、pluginの相対pathはEleventy自身の通常のproject-root解決へ委譲する。生成出力はproductionのpublic directoryへ書き込まれない。
repo外のabsolute pathや環境変数で指定された外部pathはこのoverlayの保証対象外とする。

workspaceは`PREVIEW_STATE_DIR`へ永続化しない。site runtimeの明示的なStop、idle timeoutまたはCMS shutdown時に削除する。idle timeoutは`HOMECMS_LOCAL_PREVIEW_IDLE_TIMEOUT`で指定し、初期値は30分、`0`で無効化する。browser tabのsession IDはworkspace管理に使用しない。

### editor update ordering

browserはdocumentごとの単調増加`revision`を送る。

- `revision == 0`は拒否
- server側でsite単位のrevisionを採番し、client revisionをtab間のordering判定に使わない
- 複数tabのupdateはlast-write-winsで受理する
- shadow上の対象contentが受信contentと同一ならrevision、atomic replace、metadata invalidationを行わずactivityだけ更新する
- Previewの明示的な再表示ではclient-localな同期済み判定を使わず、現在のeditor payloadを再送する。同一contentなら上記no-opで処理する
- 対象記事はtemporary fileからreplaceする

### content resource同期

初回mirrorにはarticle bundle内のpage resourceも含まれる。workspace作成後にCMS media APIでcontent directory配下へupload/deleteしたresourceは、production mutation前にmetadata invalidationを試行し、active shadow workspaceへ同じ変更を同期した後にも再度invalidateする。事前invalidationの失敗だけでproduction mutationを中止せず、最終変更後のwatch rebuildとURL map更新へ収束させる。削除resourceのwatch event取りこぼし時は対象pathの親directoryを再通知してwatch rebuildを回復させる。static resourceはproduction-linked treeを監視するためshadowへコピーせず、同じinvalidation契約だけを適用する。

static配下はHugoが元repositoryを直接参照するためshadow同期しない。static resource変更がURL mapに影響しない場合でも、Eleventyのproduction-linked treeではmetadata invalidation後のwatch rebuildを許可する。記事削除・media upload/deleteはproduction mutationとPreview同期を別の安全境界として扱い、media本体の保存・削除または記事削除が成功した後にpreview-only同期またはinvalidationが失敗した場合、APIを失敗扱いにはせずserver logへ残し、レスポンスの`local_preview_sync: false`で通知する。クライアントはproduction mutationを再試行しない。

preview-only同期が失敗した場合はsite単位の`workspace_rebuild_required`を記録する。次のPreview ingress、記事URL解決、shadow update、またはalways-on supervisorの起動・scheduled recoveryは、既存のgenerator processとshadow workspaceを`ResetRuntime`でdetachし、production repository treeからworkspaceを再生成してから処理を続ける。resetが失敗している間は状態を解除せず、status APIで診断できる。これにより、scheduled refreshがprocessだけを再起動する通常契約でも、同期失敗後だけは古いshadowを保持しない。

### site runtime activity / cleanup

Local Previewは完全にsite-scopedであり、browser tab ownership、lease、heartbeat、stale reclaimを持たない。同じsiteへの複数tabのupdateは同じshadow workspaceへlast-write-winsで適用する。update、記事URL解決、preview ingress、content resource同期はsite runtimeの`lastActivity`を更新する。

article切替ではbrowserがproduction saveとin-flight update完了を待ち、同じsite workspaceへ選択pathを反映する。generator process、Eleventy project overlay、shadow workspaceは再作成しない。serverは受理したupdateごとにserver側revisionと現在記事pathを更新し、watch rebuild後に要求pathのURLを解決する。明示的な停止またはidle timeoutではcleanup leaseがsiteのwrite gateを取得してgenerator processを停止し、workspace rootを一意なcleanup領域へrenameして論理的にdetachする。その間、ingress、update、resource同期、記事URL解決はread gateを保持し、cleanup開始時のgenerationを跨いでgate待ちした要求は503またはno-opとして古いworkspaceを再作成しない。その後のshadow directoryの物理削除は非同期で行い、準備後のHTTP/WebSocket streamingはgate外で実行する。

Eleventyでは同一contentのupdateをno-opにしてwatch rebuildを発生させない。front matterが不変の既知記事の本文変更ではincremental buildによりtemplate処理とpassthrough copyを変更分に限定し、front matter変更や記事の追加・削除、layout・data・config変更ではfull buildへ切り替える。content変更時のmetadata invalidationには対象pathを渡し、wrapperは一定時間build開始を観測できなければ対象fileのmtimeを再通知する。metadata endpointはinvalidation/build generationと最終build完了時刻を返し、URL解決timeoutやarticle not foundのserver logでbuild停滞とpath不一致を切り分けられる。既知記事をstale URLで先行表示した場合、frontendはfresh metadataのgenerationが進み、同じbuildのLiveReload通知でまだ復旧していなければ同じURLを一度だけreloadする。

Git Sync成功後はsite runtimeを既存のcleanup gateでresetする。generator processとshadow workspaceを停止・detachし、content以外のconfig、layout、asset、dependency変更も古いprocessやEleventy overlayへ引き継がない。通常siteは次回Preview requestで最新production treeからlazy startし、`always_on: true`のsiteはbackground supervisorが最新production treeから再度prewarmする。frontendはSync開始前にAutoSaveとPreviewの待機・送信中処理をdrainし、Sync中のeditor書き込みを停止する。完了後は同期中に変化した未保存状態を再評価してから、必要な記事だけをproduction treeから再ロードする。

### filesystem境界

- article/resource pathは既存`SafeJoin`境界で検証
- shadow initial copyではcontent symlinkを拒否
- Eleventy overlayのproduction directory referenceは一時overlay内だけに作成し、`content_dir`と`public_dir`はproductionから分離
- regular fileだけをmirror
- production contentはLocal Preview updateによって変更しない
- site IDは事前validation済みの値だけをworkspace pathに使用する

### Hugo Modules

Hugo Modulesで`content`をcustom mountしているsiteはlegacy `contentDir`とは異なる解決経路を使う場合がある。実blog repositoryでのsmoke test後、必要ならmount-aware方式を追加する。

## API

### editor state update

```text
POST /admin/api/preview/local
```

```json
{
  "revision": 12,
  "path": "posts/example.md",
  "frontmatter": {"title": "Draft"},
  "body": "editing...",
  "format": "yaml"
}
```

初回updateでworkspaceを作った場合、保存済みcontentを使っていた既存generator processを一度停止する。次のpreview hostname requestでshadow workspaceを含むproject-root overlayを使ってlazy startし、その後は同じsite runtimeを記事切替でも再利用してgenerator watcherが変更を拾う。

### 初回記事URL解決

```text
POST /admin/api/preview/local/navigate
```

記事選択時にCMSが記事pathを`/admin/api/preview/local/navigate`へ送り、Hugoは`hugo list all`、Eleventyは稼働中wrapperの`/__homecms_metadata`へ問い合わせて要求pathの`article_url`を返す。レスポンスには`fresh`、`metadata_status`、build generationを含める。Eleventyが既知の記事をbuild中に解決した場合はcached URLを即時返し、clientはfresh mapをバックグラウンドで待ってURL変更時だけ再遷移する。未知の記事や初回起動は同じprocessのfresh metadata/readinessを待つ。この処理はproduction content、Git、browser revisionを変更しない。network error、408/425/429、5xxに限ってclientが250ms・750msのbackoffで最大3回まで再試行し、その他の4xxは再試行しない。通常の本文編集はLiveReloadを利用し、URL関連front matter変更時は再解決する。解決失敗時はpreview rootへ黙ってフォールバックしない。

### stop

```text
POST /admin/api/preview/local/stop
```

Stop時はgenerator process groupの停止と`cmd.Wait()`完了後にworkspaceをdetachし、物理削除は非同期で行う。active shadow workspaceがなければ次のpreview requestは保存済みrepository contentを使う。Eleventyのtemporary outputも同時に削除する。`npm exec`や`mise`のwrapperがstdio pipeを保持しても待機が無期限にならないよう、process groupへのgraceful/forced signalと`WaitDelay`を共通の停止経路で管理する。

## Phase 4への契約

- Local Live Previewを開く/新規tab導線（埋め込みを主導線、新規tabをfallback）
- desktopの編集+埋め込みpreview並列表示と狭い画面での上下配置
- iframe loading、応答未確認のbest-effort表示、新規tab fallback。preview側がCMS originをtarget originに指定して`homecms-local-preview-ready`の`postMessage`を送る場合は明示的なready通知として扱う
- 記事選択時の`PreviewURLResolver`によるgenerator準拠の実ページURL解決と、解決済みURLのiframe/新規タブ表示
- Local Preview有効siteの`Edit` / `Preview` / `Split`統合。無効siteではMarkdown Previewを維持
- starting / ready / failed状態表示
- Local Previewの明示停止とsite runtime activity/idle cleanup方針
- private network/Tailscale運用例
- wildcard DNS / TLS ingress構成例
- Deployment Previewとの役割差のUI明記

## セキュリティ要点

- wildcard DNSはauthorizationではない
- preview ingressにはprivate networkまたは独立viewer authenticationを必須とする
- CMS session cookieをpreview subdomainと共有しない
- unknown site IDは拒否
- generator child environmentはallowlist
- internal generator serverはloopback bindのみ
- TLS/DNS credentialをCMSへ要求しない
- Local Live Previewはrepository内generator codeを実行するため、Markdown本文プレビューより広いtrust boundaryである

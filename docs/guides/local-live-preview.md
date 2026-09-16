# Local Live Preview設定ガイド

> Issue #32ではPhase 1〜4が実装済みです。site-scoped runtimeのstatus/stop APIと、CMS内の埋め込みpreviewを主導線とするUIを提供します。実blogとwildcard ingressを使った受け入れ確認はIssue #37で追跡します。

## 基本設定

```env
LOCAL_LIVE_PREVIEW_ENABLED=true
PREVIEW_DOMAIN=preview.example.com
PREVIEW_SCHEME=https
```

`tech` siteならpreview URLは次になります。

```text
https://tech.preview.example.com/
```

`PREVIEW_DOMAIN`には`*.`、scheme、path、portを含めません。`PREVIEW_SCHEME`は`http`または`https`だけです。

## Site Registry

```yaml
default_site: tech
sites:
  - id: tech
    repo_path: /data/repos/tech-blog
    generator: hugo
    preview:
      local_preview:
        enabled: true
        always_on: true
        refresh:
          times:
            - "04:00"
          timezone: Asia/Tokyo
```

`preview.local_preview.enabled`を省略すると`LOCAL_LIVE_PREVIEW_ENABLED`を継承します。有効siteの`GET /admin/api/sites`にはderived `preview.local_preview.url`が含まれます。

`always_on`は既定値が`false`です。`true`のsiteはCMS起動後に非同期でprewarmされ、generatorのunexpected exitをbounded exponential backoff（500ms開始、最大30秒）で自動復旧します。`enabled: false`の場合は`always_on`より優先され、Previewを起動しません。`refresh.times`は重複不可の`HH:MM`形式daily scheduleです。`refresh.timezone`にはIANA timezoneを指定し、省略時はUTCとして扱います。

有効site IDはlowercase DNS labelで、`<site-id>.<preview-domain>`全体も253文字以内の有効DNS名である必要があります。

## wildcard DNS / TLS / viewer authentication

想定DNS:

```text
*.preview.example.com -> preview ingress
```

TLS certificate、DNS-01、Cloudflare、Tailscale、Caddy/Traefik/Nginx等は外部preview ingressの責務です。

**wildcard DNSとHost validationは閲覧者認可ではありません。** Tailscale等のprivate network内に置くか、Internet reachableならCloudflare Access等の独立viewer authenticationで保護してください。CMS session cookieをpreview subdomainへ共有しません。

## Host routing

HTTPSでは次を許可します。

```text
tech.preview.example.com
tech.preview.example.com:443
```

次はfail closedします。

```text
preview.example.com
foo.tech.preview.example.com
unknown.preview.example.com
tech.preview.example.com:8443
tech.preview.example.com:evil
```

HTTP設定ではport省略または`:80`だけを許可します。HostはSite Registry lookupにだけ使います。

## Generator process

通常siteは最初のpreview hostname requestで、siteの`generator`に対応した開発サーバーをlazy startします。`always_on: true`のsiteはCMS起動後に同じ開発サーバーを非同期prewarmします。現在はHugoとEleventyに対応しています。

```text
hugo server
  --source .
  --contentDir <content-dir>
  --bind 127.0.0.1
  --port <internal-port>
  --baseURL https://tech.preview.example.com/
  --appendPort=false
  --liveReloadPort 443
  --renderToMemory
  --buildDrafts
  --buildFuture
  --buildExpired
  --watch
  --noHTTPCache
```

内部portは`14100-14999`から予約します。Hugoはloopbackだけへbindし、child environmentはgenerator allowlistを使います。

CMS shutdown開始後は新規preview processを起動せず、HTTP serverをdrainしてからchild processを停止します。

Eleventy siteでは、対象siteのlock fileから検出したpackage manager経由で、production repositoryを元にしたtemporary project-root overlayをcwdとして次のように起動します。

```text
<package-manager> exec node <CMS>/scripts/eleventy-local-preview.cjs
  --serve
  --input <project-relative-content-dir>
  --output <temporary-project-public-dir>
  --port <internal-port>
  --host 127.0.0.1
```

CMSのNodeラッパーはtemporary project-root overlayをcwdにしてEleventyのprogrammatic `watch`を実行します。overlayでは`content_dir`をshadow workspaceへ、`public_dir`をtemporary outputへ置き換え、package.json、node_modules、config、includes/layouts/data、その他のproject-root相対パスはproduction repositoryを参照します。HTTP配信とLiveReload WebSocketはCMS側のloopback serverが担当し、Eleventyの初回build前にlistenerを`127.0.0.1`へbindします。初回build完了後はEleventyのprogrammatic APIでincremental buildを有効にし、既知記事のfront matterが変わらない本文変更では変更ファイルと依存する出力だけを再処理します。front matter変更、記事の追加・削除、layout・data・configなど広い依存が疑われる変更はfull buildへ切り替え、collectionやglobal dataの結果を古いまま残しません。`/__homecms_ready`はbuild中に503、初回build完了後に200を返すため、重いsiteでも起動処理が固定秒数で同じbuildを繰り返しません。Eleventy標準Dev Serverの未指定hostや`HOST`環境変数には依存しません。

process停止はgeneratorの`cmd.Wait()`完了を成功条件とし、temporary outputのfilesystem cleanup完了を待ちません。Unix/Linuxではgenerator commandを独立したprocess groupで、WindowsではJob Objectへ割り当てます。WindowsはJob割当まで子生成を抑止するためプロセスを一時停止して起動し、`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`でwrapper配下も含めて終了させます。Stop・idle cleanup・Git Sync reset・CMS shutdownの共通経路からgraceful stopを試み、grace period後も残る場合はUnixでは`SIGKILL`、WindowsではJob Objectの強制終了へ移行します。package managerや`mise`のwrapper配下にあるNode/Eleventyも同じtreeとして終了させ、stdio pipeを保持した孤児化で`Wait()`が無期限に待たないよう待機上限も設定します。停止時のtimeoutには親PIDとprocess groupまたはJob Objectをserver logへ記録します。workspaceが所有するEleventyのproject/outputはsite runtimeのStopまたはidle cleanupで削除し、process cleanupとの二重削除や次世代workspaceとの競合を避けます。workspace外のEleventy previewは起動ごとに一意なtemporary project rootを割り当て、停止後に所有projectだけを非同期cleanupします。cleanupの遅延・失敗はserver logへ記録しますが、generator processの停止失敗とは区別します。

Eleventy設定はoverlayのproject rootで通常どおり解決します。そのため`getFilteredByGlob("src/posts/**")`、`addPassthroughCopy("src/images")`、pluginの`outputDir: "./public/img/"`のようなproject-root相対指定も、CMS側で解析・推定せずpreview側のcontent/publicを参照します。通常の記事URL解決では、稼働中wrapperが`eleventy.after`の`inputPath`/`url`を保持する`/__homecms_metadata?path=...`を利用します。body-only incremental buildの`eleventy.after`は変更分だけを返すため、wrapperは既存のmetadata mapへ差分をマージし、削除された記事だけをmapから除去します。front matterや広い依存の変更時はfull buildの結果でmap全体を更新します。workspace updateはshadow fileを書き換える直前にmetadataをinvalidateします。既知の記事はwatch build中も旧mapを`status: stale`、`fresh: false`として返し、未知の記事だけはbuild完了まで503で待機します。watch rebuildごとにmapを更新するため、resolver専用の追加full buildを発生させません。既存の直接resolver呼び出しにはJSONモードのfallbackを残します。
repo外のabsolute pathや環境変数で指定された外部pathはこのfilesystem overlayの保証対象外です。

初回buildとwatch rebuild後のmetadata URL解決の待機上限はデフォルト2分です。必要に応じて`HOMECMS_LOCAL_PREVIEW_STARTUP_TIMEOUT`へGoのduration（例: `5m`）を設定できます。Eleventyの初回build失敗時は同じ重いbuildを自動で繰り返さず、stderrの末尾を含む診断を返します。

## Reverse proxy / LiveReload

path prefixは追加しません。

```text
https://tech.preview.example.com/css/main.css
  -> http://127.0.0.1:<internal-port>/css/main.css
```

内部upstreamを指すabsolute `Location`だけを外部preview originへ補正し、HTTP Upgradeを透過してLiveReload WebSocketを通します。Eleventyの`/__homecms_ready`、`/__homecms_metadata`、`/__homecms_invalidate`はCMSとwrapper間のloopback専用制御endpointであり、preview hostnameからは404として遮断します。LiveReload用のpathは引き続きproxyします。

## 未保存editor内容

Local Live Previewが有効なsiteでは、editor変更を約250ms debounceして次へ送ります。

```text
POST /admin/api/preview/local
```

初回update時にrepositoryの`content_dir`をOS temporary directoryへmirrorします。

```text
Editor
  -> 250ms debounce
  -> shadow content workspace
  -> generator watcher
  -> rebuild / LiveReload
```

generatorの作業ディレクトリは、Hugoでは元repository、Eleventyでは一時project-root overlayです。Hugoは`--contentDir`をshadowへ差し替え、Eleventyはproject-relativeな`--input`と`--output`を使います。theme/layout/config/data/static/assets/modulesなどgeneratorが管理する規則は、それぞれ元repoまたはoverlay内のproduction referenceから読み込み、Eleventyの生成出力はtemporary directoryへ分離します。

既存の3秒autosaveは保存機能として残りますが、Local Previewの250ms update経路はproduction working tree/Git index/refへ書き込みません。

production記事のautosaveは記事内容のSHA-256 revisionを使った楽観的排他で保護します。記事GET時のrevisionを`base_revision`として保存へ送信し、Git Sync、別tab、別browser/deviceなどでproduction内容が変わっていればserverはHTTP 409を返して上書きを拒否します。frontendは409後にAutoSaveを停止し、記事の再読み込みまたはDiffで確認するよう通知します。Local Preview shadowの更新はこのproduction競合判定とは独立して動作します。

### revision / 複数tab

各updateにはbrowser documentごとの`revision`を付けます。serverはこの値をtab間のordering判定には使わず、受信したrequestをsite workspaceへ適用してserver側のrevisionを採番します。同じsiteを複数tabから編集しても`409`にはならず、preview shadowはlast-write-winsです。production contentとGitの整合性は通常のsave側で管理します。

### article bundle resource

workspace作成時点のcontent resourceは初回mirrorに含まれます。その後CMS media APIでcontent directory配下へupload/deleteした画像等もactive shadow workspaceへ同期します。Eleventyではresourceのproduction mutation前にmetadata invalidationを行い、変更中に旧URL mapを解決できないbarrierを作ります。shadow同期後にも再度invalidateして最終resourceをwatch rebuildへ反映し、削除対象のmtimeが存在しない場合は親directoryを再通知して、watcherがfilesystem eventを取りこぼしても古いmetadataを残さないようにします。

static配下は元repositoryをHugoが直接参照するためshadow同期しません。Eleventyではproduction-linkedなstatic resource変更もproduction mutation前のmetadata invalidation対象にし、media本体の保存成功後にpreview同期または再invalidationだけ失敗した場合は、media操作を失敗扱いにせずserver logへ記録します。

## site runtime activity / cleanup

Local Previewはbrowser tabを所有者として扱いません。update、記事URL解決、preview ingress、content resource同期をsite runtimeの`lastActivity`として記録します。idle timeoutは`HOMECMS_LOCAL_PREVIEW_IDLE_TIMEOUT`で設定でき、初期値は30分、`0`で無効化できます。

idle timeoutまたは明示Stopでは、site gateで新しいupdate/ingressを止めてからgenerator processを停止し、workspace rootを一意なcleanup領域へrenameして物理削除を非同期で行います。`always_on: true`のsiteはidle cleanup対象から除外されます。長寿命のHTTP/WebSocket配信前にgateを解放するため、cleanupの遅延が配信完了をブロックしません。CMS shutdownでも同じcleanup pathを利用します。

## UI

Local Live Preview panelでは次を利用できます。

- `埋め込み表示`: 記事を選択するとCMS内のsandbox付きiframeを主表示として自動表示
- `新規タブで開く`: iframeが利用できない場合や補助的な確認に使う
- `簡易Markdownを表示`: generatorを使わない補助/fallback表示
- `停止`: site runtime、generator process、shadow workspaceを停止・cleanup
- stopped / starting / ready / failedの状態表示
- `always_on`の常駐設定、supervisorのretry状態、次回refresh時刻、実効timezoneの表示

Local Live Previewが有効なsiteではheaderのview切替を次のように扱います。

- `Edit`: Editorのみを全幅表示
- `Preview`: Local Live Previewを全幅表示
- `Split`: EditorとLocal Live Previewを左右（狭い画面では上下）に表示

記事選択時はdesktopでは`Split`を初期viewにし、generatorが解決した記事ページを表示します。CMSは`slug`、`url`、permalink、page bundleの規則を推測せず、generatorのURL resolverへ解決を委譲します。Local Live Previewが無効なsiteでは、従来どおり`Preview`と`Split`の右側に簡易Markdown Previewを表示します。

viewのレイアウト（`Edit` / `Preview` / `Split`）とPreview方式（`Local` / `Markdown`）は独立しています。Local Live Previewが有効なsiteでは、Local PreviewまたはMarkdown Previewの表示領域にある切替ボタンで方式だけを変更できます。`Split`のままLocalからMarkdownへ、またはMarkdownからLocalへ切り替えられ、現在の記事とeditor内容は維持されます。Local Live Previewが無効または利用できない場合は`Local`を選択できず、Markdown Previewをfallbackとして利用します。狭い画面でもこの切替はPreview領域内に収まり、headerのview切替を変更しません。

記事選択直後の初回表示では、現在の記事をshadow workspaceへ反映した後、generator自身のURL解決結果を取得します。Hugoは`hugo list all`の`permalink`、Eleventyは稼働中wrapperの`eleventy.after` metadata map（`inputPath`/`url`）を使います。Eleventyがready済みで対象記事のlast-known URLがある場合は、`fresh: false`のcached URLをbuild完了前にiframeへ設定し、fresh metadataの取得をバックグラウンドで続けます。build後のURLがcached URLと異なる場合は新しいURLへ再遷移し、同じ場合でもstale表示中に`active_build_generation`が進んだときは同じURLを最大1回reloadして、build中に一時404だったCSS、JS、image、passthrough assetを収束させます。すでにfreshな状態、同じbuild generation、またはEleventy LiveReloadが同じbuildをreload済みの場合は追加reloadしません。初回build前・新規記事などcached URLがない場合は従来どおりfresh metadataを待ちます。取得したURLはpath、query、fragmentを保持したままLocal Preview originへ変換し、iframeと新規タブへ直接設定します。CMSはslug、`url`、permalink、page bundle、Data Cascade、paginationなどの規則を再実装しません。通常の本文編集ではiframeの現在URLを維持してgeneratorのwatch/live reloadを利用し、URLに影響するfront matter変更時だけ再解決します。

初回URL解決のnetwork error、408/425/429、5xxは250ms・750msのbackoffで最大3試行します。その他の4xxは再試行せず、エラー状態を表示します。URLを解決できない場合はpreview rootへフォールバックしません。

iframeの読み込み中はloading表示を出し、`load`または対応するpreview bridgeのready通知を一定時間確認できない場合は、エラーと「新規タブで開く」fallbackを表示します。これはbest-effortの判定であり、CSPや`X-Frame-Options`などによるiframe拒否をブラウザAPIだけで確実に判定するものではありません。埋め込み表示はボタンから閉じられ、記事を切り替えると再び自動表示されます。狭い画面では編集画面とpreviewを上下に配置します。

preview側を管理できる場合は、正常表示後に親ウィンドウへ `window.parent.postMessage({ type: 'homecms-local-preview-ready' }, '<CMS origin>')` を送ると、CMSが明示的なready通知として扱います。第2引数のtarget originはpreview originではなく、親フレームであるCMSのorigin（例: `https://cms.example.com`）を指定し、`*`は使用しません。

### iframe埋め込み

preview hostnameはCMSとは別originなので、iframe埋め込み自体は可能です。CMSはpreview subdomainへsession cookieを共有しません。

ただし、外部preview ingressが次のheaderでframeを禁止している場合は埋め込めません。

```text
X-Frame-Options: DENY
X-Frame-Options: SAMEORIGIN
Content-Security-Policy: frame-ancestors 'none'
```

Cloudflare Access等のviewer authenticationのlogin画面がiframeを拒否する構成もあります。そのため**埋め込みを主導線にしつつ、新規タブ表示を必ずfallbackとして残します**。

CMS側iframeにはsandboxを付け、top-level navigation等を許可しません。Hugo/LiveReloadに必要なscriptとsame-origin権限だけを許可します。

## article/site切替とcleanup

article切替では、切替開始時点からeditorとfront matterを編集不可にし、現在記事のproduction保存とin-flight Local Preview updateの完了を待ってから、同じsite workspaceへ新しい記事pathを反映します。production保存の失敗はデータ保護のため切替を中止しますが、Preview flushの一時的な失敗はwarningを表示して切替を継続し、新しい記事側で再同期します。高速な連続選択では最新のload requestだけがpath、editor、Markdown Preview、Local Previewの結果をcommitし、古いfetch responseは破棄します。新しい記事を取得するまで保存対象のcurrent pathは切り替えず、旧記事の編集中payloadが要求中の別記事pathへ保存されないようにします。generator process、project overlay、shadow workspaceは再利用し、watch rebuildとgenerator準拠のURL解決だけを行います。browser tab ownershipを持たないため、記事切替でstop/releaseは実行しません。Previewを開く、または埋め込みpreviewを再表示する明示操作では、clientの同期済み判定やstatus pollingに依存せず、現在のeditor payloadを必ずsite-scoped shadowへ再同期してからURLを解決します。複数tab共有のshadowとclient状態がずれていてもこの再同期で収束し、同一contentの場合はserver-side no-opにより不要なupdate/invalidation/buildを発生させません。

UIの明示的な停止では、clientがdebounce済みの更新をキャンセルし、送信済みの更新完了を待ってからsite runtimeを停止します。idle timeout、CMS shutdownを含め、site runtimeは次の順で終了します。

```text
SIGTERM -> generator process group
  -> grace period
  -> SIGKILL (if still running)
  -> cmd.Wait() / process tree termination
  -> workspace root detach/rename
  -> shadow workspace delete (async)
```

CMS shutdownでもgenerator child停止後にtemporary workspaceとEleventy temporary outputを削除します。workspaceは`PREVIEW_STATE_DIR`へ永続化しません。

cleanup中に到着またはcleanup待ちになった古いpreview requestはgenerationで無効化し、停止完了直後にworkspaceやgeneratorを復活させません。cleanup後に新しく到着したrequestだけがsite runtimeを再開できます。

## Git Sync後のreset

`POST /admin/api/sync`がproduction repositoryのpullに成功した場合、対象siteのLocal Live Previewも同じcleanup gateでresetします。generator processを停止し、Eleventyのproject overlayを含むshadow workspaceをdetachして非同期cleanupするため、contentだけでなくlayouts、config、static、package.json、lockfileなどの変更も次回Preview起動時に反映されます。Sync開始時にはfrontendの3秒AutoSaveタイマーとPreviewのdebounceを停止し、すでに送信済みのAutoSave・Local Preview更新が完了するまで待機します。Sync中はeditor入力とSync操作を無効化し、保存・作成・削除・Publish・デプロイプレビュー更新の新規mutationも拒否するため、古いpayloadでreset後のworkspaceを復活させたり、同期中の編集を同期後に上書きしたりしないようにします。

Git Sync自体が成功してもPreview resetに失敗する場合があります。そのときAPIは`local_preview_reset: false`を返し、productionのSync成功とPreviewの失敗を分離して表示します。frontendはPreview URLとiframeをresetするため、server logを確認したうえで次回Preview表示時に再起動できます。Preview resetはproduction working treeやGit stateを変更しません。Sync完了後はeditorの未保存状態を再評価し、cleanなら現在の記事をproduction treeから再ロードします。Sync中に編集が発生した場合（通常は入力を無効化しています）は未保存内容を自動破棄せず保持し、Git Sync後にPreviewを開くとその現在payloadがshadowへ同期されます。remoteで削除されていた記事に未保存内容がある場合も、復旧できるようeditorは保持します。

## Hugo Modules

Hugo Modulesで`content`をcustom mountしているsiteは実blog repositoryでsmoke testしてください。必要ならmount-awareなpreview方式を追加します。

## API

editor update:

```text
POST /admin/api/preview/local
```

記事選択後の初回URL解決:

```text
POST /admin/api/preview/local/navigate
```

```json
{
  "path": "posts/example.md"
}
```

このAPIはsite単位のshadow workspaceから要求された記事pathを使ってgeneratorの実ページURLを解決します。成功時は`article_url`とserver側の`revision`を返し、production content、Git working tree、browserのrevisionは変更しません。解決に失敗した場合はエラーを返し、preview rootへフォールバックしません。

lifecycle:

```text
GET  /admin/api/preview/local/status
POST /admin/api/preview/local/stop
```

すべて既存admin auth + CSRF境界の内側です。status APIはprocess、workspace、last activityだけを返し、browser tabのowner情報を持ちません。

## 旧preview方式との違い

`PREVIEW_URL`、`HUGO_SERVER_PORT`、`HUGO_SERVER_BIND`はIssue #30以前のpath-prefix preview用legacy設定で、新Local Live Previewには使用しません。

```text
旧: /admin/preview/tech/...
新: https://tech.preview.example.com/...
```

詳細は[Local Live Preview設計](../architecture/local-live-preview-design.md)を参照してください。

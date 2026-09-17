# 設定ガイド

HomeCMSの詳細な設定オプションについて説明します。

## 環境変数

### 必須設定

#### `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET`

GitHub OAuth Appの認証情報です。

1. [GitHub Developer Settings](https://github.com/settings/developers) にアクセス
2. "New OAuth App" をクリック
3. 設定を入力:
   - **Homepage URL**: アプリケーションのURL
   - **Authorization callback URL**: `{APP_URL}/admin/auth/callback`

#### `SESSION_SECRET`

セッションCookieの署名鍵と暗号化鍵の導出に使用する、32文字以上の秘密値です。

```bash
# 生成例
openssl rand -base64 32
```

**注意**: `GIN_MODE=release`では必須です。開発モードで設定しない場合は起動時にランダムなキーを生成するため、サーバー再起動でセッションが無効になります。

### 認証・セキュリティ

#### `ALLOWED_GITHUB_USERS`

CMSへのアクセスを許可するGitHubユーザー名のリスト(カンマ区切り)。

```env
# 特定のユーザーのみ許可
ALLOWED_GITHUB_USERS=user1,user2,user3

# 1人以上のユーザー指定が必須
ALLOWED_GITHUB_USERS=user1
```

未設定または空の場合、CMSは安全のため起動を拒否します。ローカル開発で全GitHubユーザーを明示的に許可する場合だけ、次を設定できます。

```env
ALLOW_ALL_GITHUB_USERS=true
```

この設定を本番環境で使用してはいけません。

#### `GITHUB_OAUTH_SCOPES`

GitHubへのアクセス権限を指定します。

| スコープ | 説明 |
|---------|------|
| `public_repo` | 公開リポジトリのみ (デフォルト・推奨) |
| `repo` | 公開・非公開リポジトリ両方 |

```env
# プライベートリポジトリを使用する場合
GITHUB_OAUTH_SCOPES=repo
```

**重要**: スコープを変更した場合、既存ユーザーは再ログインが必要です。

### アプリケーション設定

#### `PORT`

サーバーが待ち受けるポート番号。デフォルト: `8080`

Docker構成ではコンテナ内の`PORT`を`8080`に固定します。ホスト側のloopback公開ポートは`HOMECMS_HOST_PORT`で変更してください。

#### `APP_URL`

アプリケーションの公開URL。OAuth リダイレクトやプレビューURLの生成に使用。

```env
# ローカル開発
APP_URL=http://localhost:8080

# 本番環境
APP_URL=https://cms.example.com
```

#### `REPO_PATH`

サイトリポジトリのパス。相対パスまたは絶対パスで指定。Hugoを使う場合はHugoサイト、Eleventyを使う場合はEleventyサイトを指定します。

```env
# 相対パス (hugo-cmsディレクトリからの相対)
REPO_PATH=./repo

# 絶対パス
REPO_PATH=/var/www/my-hugo-site
```

#### `SITE_GENERATOR`

対象サイトのジェネレーター。デフォルトは`hugo`です。

```env
SITE_GENERATOR=hugo
# または
SITE_GENERATOR=eleventy
```

`eleventy`を指定する場合、対象リポジトリには`package.json`とlockファイル
（`package-lock.json`、`pnpm-lock.yaml`、`yarn.lock`など）が必要です。
CMSは初期対応として任意のnpm scriptではなく、lockfileに対応するpackage managerでローカル依存のEleventyを実行します。

#### `GENERATOR_RUNTIME`

Hugo/Eleventyなどのgeneratorコマンドをどのruntimeで実行するかを指定します。デフォルトは`direct`です。

```env
# 従来通り、PATH上のhugo/npm/pnpm等を直接使う
GENERATOR_RUNTIME=direct

# 対象リポジトリのmise設定を使う
GENERATOR_RUNTIME=mise
```

`mise`を指定すると、CMSはgeneratorコマンドを次の形で実行します。

```bash
mise exec -C <repo_path> -- hugo ...
```

Docker運用では`GENERATOR_RUNTIME=mise`を推奨します。ホストではなくコンテナ内のmiseが、各サイトリポジトリの`mise.toml` / `.mise.toml` / `.tool-versions`を読みます。

app起動時には`mise install`やNode.js依存のインストールを行いません。管理者が`HOMECMS_REPOS`へ明示したリポジトリだけを、secret-freeな`tool-bootstrap` one-shot serviceで事前準備します。詳細は[Docker + mise デプロイガイド](docker-mise-deployment.md)を参照してください。

### Docker Compose専用設定

次の値はGoアプリケーションの設定ではなく、rootの`compose.yml`がbuild、host port、one-shot bootstrapを構成するために使用します。

| 変数 | 説明 | 既定値 |
|---|---|---|
| `HOMECMS_UID` | app/tool-bootstrapの非root UID。Linuxではhost userに合わせる | `10001` |
| `HOMECMS_GID` | app/tool-bootstrapの非root GID。Linuxではhost groupに合わせる | `10001` |
| `HOMECMS_HOST_PORT` | hostの`127.0.0.1`へ公開するポート | `8080` |
| `HOMECMS_REPOS` | bootstrapを許可するcontainer内repo絶対パス。Unixの`:`区切り | なし（要指定） |

`HOMECMS_REPOS`はカンマや空白区切りではありません。`/data/repos`の自動探索も行わないため、Site Registryへ追加したrepoも、実行を承認したうえで別途allowlistへ列挙します。

#### `CONTENT_DIR` / `STATIC_DIR` / `PUBLIC_DIR`

対象リポジトリ内の主要ディレクトリ。Hugoの標準構成では変更不要です。

```env
CONTENT_DIR=content
STATIC_DIR=static
PUBLIC_DIR=public
```

Eleventyなどで入力・出力ディレクトリが異なる場合は明示します。

```env
SITE_GENERATOR=eleventy
CONTENT_DIR=src
STATIC_DIR=public-assets
PUBLIC_DIR=_site
```

#### `SITES_CONFIG_PATH`

複数サイト対応へ向けたSite Registry設定ファイル。未指定の場合は従来通り、環境変数で定義した単一のdefault siteだけを使います。

```env
SITES_CONFIG_PATH=./sites.yml
```

設定例:

```yaml
default_site: techblog
sites:
  - id: techblog
    name: Tech Blog
    repo_path: D:/sites/techblog
    generator: hugo
    runtime: mise
    content_dir: content
    static_dir: static
    public_dir: public
    preview:
      local_preview:
        enabled: true
    snippet_paths:
      - .vscode/md.code-snippets

  - id: notes
    name: Notes
    repo_path: D:/sites/notes
    generator: eleventy
    runtime: mise
    content_dir: src
    static_dir: public-assets
    public_dir: _site
    preview:
      local_preview:
        enabled: false
    snippet_paths:
      - .vscode/md.code-snippets
```

Site Registryを設定すると、管理画面のサイドバーにサイトセレクタが表示されます。記事、CMS設定、メディア、スニペット、Git操作、プレビューは選択中サイトを対象にします。

HTTP APIでは、`?site=<site_id>`または`X-CMS-Site: <site_id>`で対象サイトを指定できます。未指定の場合は`default_site`が使われます。

Site Registryは起動時にstrict validationされます。未知のキー、重複した`id`、`id`または`repo_path`の欠落、未対応の`generator`/`runtime`、リポジトリ外を指す`content_dir`・`static_dir`・`public_dir`・media directoryは起動エラーになります。環境変数だけで構成するlegacyのsingle-site設定では、従来のdefault補正と互換性を維持します。

### サイト内CMS設定

各サイトリポジトリでは、リポジトリ直下の`.homecms.yml`を優先して読み込みます。既存互換として`<static_dir>/admin/config.yml`も利用できますが、両方が存在する場合は`.homecms.yml`が優先されます。

最小例:

```yaml
version: 1

content:
  collections:
    - name: posts
      label: Posts
      folder: content/posts
      path: "{{slug}}"
      extension: md
      frontmatter: yaml
      fields:
        - { name: slug, label: Slug, widget: string }
        - { name: title, label: Title, widget: string }
        - { name: body, label: Body, widget: markdown }

media:
  folder: static/images
  public_path: /images
```

`media.folder`はリポジトリルート基準です。`static_media_dir` / `STATIC_MEDIA_DIR`を明示していないサイトでは、この値がstatic media modeの保存先になります。Issue #112では通常のMarkdown記事に対するcollection単位のArticle Media解決を追加しています。

通常のMarkdown記事でArticle Mediaを使う場合は、collectionに `media_folder: "{{dirname}}"` または `media_folder: "{{dirname}}/images"` を指定します。`{{dirname}}`は記事Markdownのディレクトリに展開され、一覧・upload・deleteの対象とMarkdown挿入pathで共通して使われます。既存サイトでは `ARTICLE_MEDIA_DIR` またはSite Registryの `article_media_dir` も利用できます。

旧 `static/admin/config.yml` のcollectionにある Netlify/Sveltia形式の `media_folder` は保存先として解釈せず、legacy設定では `ARTICLE_MEDIA_DIR` またはSite Registryの `article_media_dir` を使用します。未指定のpage bundleはバンドル直下を対象にします。

`content.collections[].path`で`{{slug}}`のような変数を使う場合は、作成フォームから値を送信できるように同名のfieldを定義してください。

CMSは設定読み込み時に、collection名・folder・path変数・`preview.url_field`・media folderなどを検査します。注意点がある場合は`GET /admin/api/config`の`_cms.warnings`に入り、管理画面のサイドバーにも表示されます。

既存互換の`<static_dir>/admin/config.yml`は引き続き読み込めますが、新規設定と新機能は`.homecms.yml`を基準にします。legacy configを使用しているサイトでは、移行を促すwarningが表示されます。

### Preview

プレビューは次の3段階で扱います。

1. **本文プレビュー**: generatorを実行せず、保存前のMarkdownをsanitizeして即時表示する
2. **Local Live Preview**: 実際のgenerator/theme/layout/shortcode/CSS/JSを使う編集中確認
3. **デプロイプレビュー**: 外部buildで特定commitを公開前に最終確認する

Issue #32ではPhase 1/2でLocal Live Previewの設定・hostname routing・Hugo process/proxy/LiveReload基盤まで実装済みです。Phase 3 (#35)では未保存editor入力をephemeral shadow content workspaceへ約250msで反映し、generator watcherへつなぎます。Local Preview update自体はproduction working tree/Git index/refを書き換えず、content配下のmedia upload/deleteもactive workspaceへ同期します。Phase 4ではsite runtimeの状態表示と操作を追加し、Issue #38でCMS内の埋め込みpreviewを主導線、新規タブをfallbackとするUIへ整理しています。Issue #40ではLocal Preview有効siteの`Edit` / `Preview` / `Split`をLocal Live Preview中心に統合しています。Issue #42では記事選択直後の初回表示を追加し、Issue #44では`PreviewURLResolver`を通じてHugo自身のURL解決結果をiframeと新規タブへ直接表示するようにしています。Issue #46ではEleventyの`--serve`、project-root overlay、`--to=json` metadata URL解決を同じ導線へ追加し、Issue #48ではEleventy Programmatic APIの実際のUserConfig形に合わせたhook登録と実Eleventy統合テストを追加しています。Issue #50ではcleanなproduction outputからの実サイト相当fixtureで、config.dir・collection glob・passthrough・plugin生成物とproduction output不変を検証しています。Issue #58ではbrowser tab ownership、lease、heartbeat、stale reclaimを廃止し、Local Previewをsite-scoped runtimeへ統一しています。Issue #99ではPreview headerに状態を常時表示しつつ、runtime操作とdiagnostic情報を折りたたみパネルへまとめ、failed遷移時に詳細を自動展開するようにしています。Issue #100ではviewのレイアウトとPreview方式を分離し、`Preview` / `Split`の状態を維持したままLocal PreviewとMarkdown Previewを切り替えられるようにしています。

Issue #60ではEleventyの重複updateを抑制し、同一contentのserver no-op、watcher再通知、metadata診断を追加しています。Issue #64では記事削除とmedia/resource同期にもmetadata invalidationと削除時のparent directory recoveryを適用しています。Issue #68では既知記事のlast-known metadataをbuild中もstaleとして返し、Eleventy記事切替をcached URLへ先に遷移してからfresh URLへ収束する方式にしています。Issue #82では記事削除・media upload/deleteのproduction mutationとLocal Preview同期を分離し、Preview障害時も本番変更を成功させ、`local_preview_sync: false`で再試行不要のdegraded状態を返します。同期失敗時はworkspaceを再構築対象として記録し、次回Preview accessまたはsupervisor recoveryでproduction treeへ収束させます。Issue #101ではfront matterが不変の既知記事の本文変更だけEleventy watchのincremental buildを使い、template処理・passthrough copy・metadata map更新を変更分に限定します。front matter変更、記事の追加・削除、layout・data・config変更はfull buildへ切り替えます。Issue #108ではPublishをDeployment Previewパネルに依存しないヘッダー操作として常時表示し、readyなプレビューがある場合は確認済みcommitから、無効・更新中・失敗・古い状態では現在の編集内容からdraft PRを作成します。Issue #110ではGit Syncをremote fetchとlocal dirty変更のreconcileに変更し、merge済みの同一変更だけをcleanに収束させ、未Publish変更を保持し、競合時は明示的なConflictとして通知します。

Site Registryでサイトごとに設定します。

```yaml
sites:
  - id: techblog
    repo_path: D:/sites/techblog
    preview:
      markdown:
        enabled: true
      local_preview:
        enabled: true
        # Optional: prewarm and supervise this site's generator.
        always_on: true
        refresh:
          times: ["04:00"]
          timezone: Asia/Tokyo
      deployment:
        provider: cloudflare_pages
        access_protected: true
        cloudflare_pages:
          account_id: 0123456789abcdef0123456789abcdef
          project_name: techblog
          token_env: TECHBLOG_CLOUDFLARE_API_TOKEN
```

`markdown.enabled`は既定で`true`です。本文プレビューはGFMをsanitizeして表示し、Hugo shortcode、layout、サイト固有CSS/JavaScriptは再現しません。relative imageは記事bundle、root-relative imageは`static_dir`から解決し、既存の許可済みmediaだけを認証付きrouteで表示します。

`local_preview.enabled`を省略した場合は`LOCAL_LIVE_PREVIEW_ENABLED`を継承します。有効なsiteには`PREVIEW_SCHEME`、site ID、`PREVIEW_DOMAIN`から`preview.local_preview.url`をderived valueとして生成します。Local Live Previewを有効にするsite IDはlowercase DNS labelである必要があり、生成後の`<site-id>.<preview-domain>`全体も253文字以内の有効なDNS名でなければなりません。

`local_preview.always_on`は既定値が`false`です。`true`にするとCMS起動後に対象siteのPreview generatorを非同期でprewarmし、unexpected exitを500msから最大30秒のbounded exponential backoffで再起動します。`enabled: false`が常に優先されます。`refresh.times`は`HH:MM`形式のdaily scheduleで、重複値は拒否されます。`timezone`はIANA timezoneで、省略時はUTCです。scheduleの実効timezoneと次回時刻はLocal Preview status APIで確認できます。

Local Live Previewのwildcard DNS、TLS、DNS-01、Tailscale、外部reverse proxyはpreview ingress側の責務です。**wildcard DNSやHost validationは閲覧者認可ではありません。** Local Live Preview ingressは必ず、Tailscale等のprivate network内に置くか、Internet reachableな場合はCloudflare Access等の独立viewer authenticationで保護してください。CMSのsession cookieをpreview subdomainへ共有して認証に使う設計にはしません。

詳細は[Local Live Preview設定ガイド](local-live-preview.md)と[Local Live Preview設計](../architecture/local-live-preview-design.md)を参照してください。

`deployment.provider`を省略したサイトでも編集と本文プレビューは利用できます。`cloudflare_pages`を使う場合、`account_id`、`project_name`、`token_env`が必須です。`token_env`はtoken値ではなく、tokenを格納した環境変数名です。token値はYAMLへ書かず、browserにも返りません。

`deployment.access_protected: true`は、Cloudflare Accessを運用側で設定済みであることをCMSへ申告する値です。CMSがAccess policyを作成するわけではありません。`false`では未公開contentがpublic deployment previewに出る可能性をUIで警告します。

単一サイトを環境変数だけで設定する場合は次を使用できます。

```env
MARKDOWN_PREVIEW_ENABLED=true
LOCAL_LIVE_PREVIEW_ENABLED=true
PREVIEW_DOMAIN=preview.example.com
PREVIEW_SCHEME=https

PREVIEW_DEPLOYMENT_PROVIDER=cloudflare_pages
CLOUDFLARE_PAGES_ACCOUNT_ID=0123456789abcdef0123456789abcdef
CLOUDFLARE_PAGES_PROJECT_NAME=techblog
CLOUDFLARE_PAGES_API_TOKEN_ENV=CLOUDFLARE_API_TOKEN
CLOUDFLARE_API_TOKEN=secret-value
PREVIEW_DEPLOYMENT_ACCESS_PROTECTED=true
PREVIEW_STATE_DIR=./data/preview-deployments
```

`PREVIEW_STATE_DIR`はcommit SHA、deployment ID、status、URL、cleanup状態を保存します。tokenは保存しません。複数instanceで運用する場合は、全instanceから同じ永続storageを参照できる配置と排他制御を別途設計してください。

### site registry項目

| 項目 | 必須 | 説明 |
|---|---:|---|
| `id` | はい | サイトID。UI、API、draft state分離で使用。Local Live Preview有効時はlowercase DNS label制約も適用 |
| `name` | いいえ | UI表示名。未指定時は`id` |
| `repo_path` | はい | 対象リポジトリ |
| `generator` | いいえ | `hugo`または`eleventy` |
| `runtime` | いいえ | `direct`または`mise`。未指定時は`GENERATOR_RUNTIME` |
| `content_dir` | いいえ | Markdown記事のルート。デフォルト`content` |
| `static_dir` | いいえ | 静的ファイルのルート。デフォルト`static` |
| `public_dir` | いいえ | build出力先。デフォルト`public` |
| `preview.markdown.enabled` | いいえ | 安全な本文プレビュー。デフォルト`true` |
| `preview.local_preview.enabled` | いいえ | Local Live Preview。省略時は`LOCAL_LIVE_PREVIEW_ENABLED` |
| `preview.local_preview.always_on` | いいえ | generatorを起動時から常駐監視。デフォルト`false`。`enabled: false`が優先 |
| `preview.local_preview.refresh.times` | いいえ | `HH:MM`形式のdaily refresh時刻配列。重複不可 |
| `preview.local_preview.refresh.timezone` | いいえ | IANA timezone。省略時は`UTC` |
| `preview.deployment.provider` | いいえ | 空または`cloudflare_pages` |
| `preview.deployment.access_protected` | いいえ | Deployment PreviewのAccess設定済み申告。デフォルト`false` |
| `preview.deployment.cloudflare_pages.*` | provider使用時 | `account_id`、`project_name`、`token_env` |
| `snippet_paths` | いいえ | スニペットファイル。相対パスは`repo_path`基準 |

### スニペット設定

#### `SNIPPET_PATHS`

Markdown編集時に使用するスニペットファイルのパス。カンマ区切りで複数指定可能。
ファイル形式は VS Code のスニペット形式 (`.code-snippets` または `.json`) に準拠します。
`scope` プロパティに `markdown` が含まれるか、`scope` が未指定（グローバル）のスニペットのみが読み込まれます。

単一サイト構成のデフォルト: `<REPO_PATH>/.vscode/md.code-snippets`

Site Registry利用時は、サイトごとに`snippet_paths`を指定できます。未指定の場合は各サイトの`<repo_path>/.vscode/md.code-snippets`を読み込みます。

```env
# 複数ファイルを指定
SNIPPET_PATHS=repo/.vscode/global.code-snippets,repo/.vscode/md.code-snippets
```

Site Registryでの例:

```yaml
sites:
  - id: techblog
    repo_path: D:/sites/techblog
    snippet_paths:
      - .vscode/global.code-snippets
      - .vscode/md.code-snippets
```

### メディア設定

#### `MAX_UPLOAD_SIZE_MB`

アップロード可能なファイルサイズ (MB単位)。デフォルト: `10`

```env
# 50MBまで許可
MAX_UPLOAD_SIZE_MB=50
```

#### `ARTICLE_MEDIA_DIR`

記事のPage Bundle内で画像を保存するサブディレクトリ。

```env
# content/posts/my-post/src/image.jpg に保存
ARTICLE_MEDIA_DIR=src

# content/posts/my-post/image.jpg に保存 (空の場合)
ARTICLE_MEDIA_DIR=
```

#### `STATIC_MEDIA_DIR`

`static/` 内で画像を保存するサブディレクトリ。

```env
# static/images/uploaded.jpg に保存
STATIC_MEDIA_DIR=images

# static/uploaded.jpg に保存 (空の場合)
STATIC_MEDIA_DIR=
```

### 廃止したローカルpreview設定

`PREVIEW_URL`、`HUGO_SERVER_PORT`、`HUGO_SERVER_BIND`およびSite Registryの同名項目はIssue #30以前のpath-prefix型ローカルpreview用です。新しいLocal Live Previewの公開URLやport管理には使用しません。新規設定では`LOCAL_LIVE_PREVIEW_ENABLED`、`PREVIEW_DOMAIN`、`PREVIEW_SCHEME`、`preview.local_preview.enabled`を使用してください。

### Git設定

#### `GIT_USER_NAME` / `GIT_USER_EMAIL`

コミット時に使用するGit identity。

```env
GIT_USER_NAME="HomeCMS Bot"
GIT_USER_EMAIL="bot@homecms.local"
```

#### `GIT_BRANCH`

操作対象のGitブランチ。デフォルト: `main`

#### `GIT_REMOTE`

リモートリポジトリ名。デフォルト: `origin`

指定したリモートのURLは`https://github.com/owner/repository.git`形式である必要があります。SSH形式や認証情報を埋め込んだURLは、ログインユーザー以外の認証情報利用やトークン送信先のすり替えを防ぐため拒否されます。

## タイムアウト設定

以下のタイムアウトはソースコード内で定義されています:

| 操作 | タイムアウト | 説明 |
|------|-------------|------|
| Gitローカルコマンド | 60秒 | status, diff等 |
| Gitネットワーク操作 | 5分 | push, pull |
| Hugoビルド | 5分 | サイト全体のビルド |
| Hugo新規コンテンツ | 60秒 | `hugo new` コマンド |
| GitHubトークン検証 | 5分 | 定期的なトークン有効性確認 |

GitHubトークン検証は状態を`valid`、`invalid`、`unavailable`に分けます。GitHub APIが`401`を返した場合だけsessionを失効し、`403`、`429`、`5xx`、timeout/DNS/TLSなどの一時障害ではsessionを維持して検証時刻を更新しません。再検証不能はtoken invalidの証拠ではないため、GitHubから明確なinvalid応答を受けるまで再試行します。

Local Live PreviewのEleventy初回buildとwatch rebuild後のmetadata URL解決の待機上限はデフォルト2分です。重いsiteでは`HOMECMS_LOCAL_PREVIEW_STARTUP_TIMEOUT=5m`のようにGoのduration形式で変更できます。Eleventyの初回build失敗時は同じbuildを自動再試行しません。

`HOMECMS_*`が標準の設定名です。既存環境向けに`HUGO_CMS_*`（`UID`、`GID`、`HOST_PORT`、`REPOS`、Local Live Previewのtimeout）はフォールバックとして利用できますが、標準名が設定されている場合は標準名を優先し、旧名の利用時はdeprecated warningを記録します。

## 設定例

### ローカル開発

```env
GITHUB_CLIENT_ID=Iv1.xxxxxxxx
GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxx
SESSION_SECRET=dev-secret-key-change-in-production
ALLOWED_GITHUB_USERS=your-username

PORT=8080
APP_URL=http://localhost:8080
REPO_PATH=./repo

MARKDOWN_PREVIEW_ENABLED=true
```

### 本番環境 (単一ユーザー)

```env
GITHUB_CLIENT_ID=Iv1.xxxxxxxx
GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxx
SESSION_SECRET=32文字以上のランダムな文字列

ALLOWED_GITHUB_USERS=your-username

PORT=8080
APP_URL=https://cms.example.com
REPO_PATH=/var/www/hugo-site

MAX_UPLOAD_SIZE_MB=20
ARTICLE_MEDIA_DIR=images
STATIC_MEDIA_DIR=uploads

GIT_USER_NAME="Your Name"
GIT_USER_EMAIL="you@example.com"
GIT_BRANCH=main
```

### Docker環境

```env
APP_URL=https://cms.example.com
GIN_MODE=release

GITHUB_CLIENT_ID=Iv1.xxxxxxxx
GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxx
SESSION_SECRET=replace-with-at-least-32-random-characters
ALLOWED_GITHUB_USERS=your-username

REPO_PATH=/data/repos/techblog
SITE_GENERATOR=hugo
GENERATOR_RUNTIME=mise
HOMECMS_REPOS=/data/repos/techblog

HOMECMS_HOST_PORT=8080
HOMECMS_UID=1000
HOMECMS_GID=1000

MARKDOWN_PREVIEW_ENABLED=true
```

`.env`は必須です。appのcontainer内`PORT=8080`はCompose側で固定されるため、Docker用`.env`では変更しません。`tool-bootstrap`はこのファイルからallowlist等を補間しますが、app用のsecret環境変数をcontainerへ受け取りません。

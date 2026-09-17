# API リファレンス

HomeCMSのREST API仕様です。

## 認証

すべての `/admin/api/*` エンドポイントは認証が必要です。
本文preview、deployment preview、media rawも認証済みadmin API配下で提供されます。

### セッション認証

GitHub OAuthでログイン後、セッションCookieが発行されます。

`TokenValidation`は5分以上検証していないsessionに対してGitHub API `/user`を再検証します。`200`では検証時刻を更新し、`401 Unauthorized`だけをtoken invalidとしてsessionを失効させます。`403`、`429`、`5xx`、timeout/DNS/TLSなどのtransport errorはGitHubまたはネットワークの一時障害とみなし、sessionを維持して検証時刻を更新しません。unavailable時は`token_validation_retry_at`に60秒のbackoffを記録し、その間のrequestでは再検証しません。backoff後に再試行されるため、一時障害だけでlogoutされることはありません。

### CSRF保護

`POST`, `PUT`, `DELETE` リクエストには `X-CSRF-Token` ヘッダーが必要です。

```javascript
// CSRFトークンの取得
const response = await fetch('/admin/api/csrf-token');
const { token } = await response.json();

// リクエスト時にヘッダーに含める
fetch('/admin/api/article', {
    method: 'PUT',
    headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': token
    },
    body: JSON.stringify(data)
});
```

---

## サイト指定

Site Registryを使う場合、site-aware APIはクエリパラメータまたはヘッダーで対象サイトを指定できます。

```http
GET /admin/api/articles?site=techblog
X-CMS-Site: techblog
```

両方が指定された場合はクエリパラメータが優先されます。未指定の場合は`default_site`が使われます。

対象API:

- `/admin/api/articles`
- `/admin/api/article`
- `/admin/api/create`
- `/admin/api/delete`
- `/admin/api/diff`
- `/admin/api/config`
- `/admin/api/snippets`
- `/admin/api/sync`
- `/admin/api/publish`
- `/admin/api/media`
- `/admin/api/media/delete`
- `/admin/api/media/raw`

---

## 認証API

### GET /admin/auth/login

GitHub OAuth認証を開始します。

**レスポンス**: GitHubの認証ページへリダイレクト

### GET /admin/auth/callback

OAuth認証コールバック。GitHubから呼び出されます。

**クエリパラメータ**:
- `code`: 認証コード
- `state`: CSRF用ステート

**レスポンス**: `/admin` へリダイレクト

### GET /admin/auth/logout

セッションを破棄してログアウトします。

**レスポンス**:
```json
{
    "status": "ok",
    "log": "Logged out"
}
```

### GET /admin/api/csrf-token

CSRFトークンを取得します。

**レスポンス**:
```json
{
    "token": "abc123..."
}
```

---

## 記事API

### GET /admin/api/articles

全記事の一覧を取得します。

**レスポンス**:
```json
[
    {
        "path": "posts/2026-01-10-hello/index.md",
        "title": "Hello World",
        "date": "2026-01-10T12:00:00Z",
        "draft": true,
        "dirty": false
    },
    // ...
]
```

### GET /admin/api/article

単一の記事を取得します。

**クエリパラメータ**:
- `path` (必須): 記事のパス (content相対)

**レスポンス**:
```json
{
    "path": "posts/2026-01-10-hello/index.md",
    "frontmatter": {
        "title": "Hello World",
        "date": "2026-01-10T12:00:00Z",
        "draft": true,
        "tags": ["blog", "hello"]
    },
    "body": "# Hello\n\nThis is my first post.",
    "format": "yaml",
    "revision": "sha256:..."
}
```

**エラーレスポンス**:
```json
{
    "status": "error",
    "code": "NOT_FOUND",
    "message": "Article not found"
}
```

### POST /admin/api/article

記事を保存します。

**リクエストボディ**:
```json
{
    "path": "posts/2026-01-10-hello/index.md",
    "frontmatter": {
        "title": "Updated Title",
        "date": "2026-01-10T12:00:00Z",
        "draft": false
    },
    "body": "# Updated Content\n\nNew content here.",
    "base_revision": "sha256:..."
}
```

**レスポンス**:
```json
{
    "status": "saved",
    "revision": "sha256:..."
}
```

`base_revision`はGETで取得した記事のrevisionです。サーバー上の内容が変更されている場合は、記事を上書きせずHTTP 409を返します。

```json
{
    "status": "error",
    "code": "CONFLICT",
    "message": "Article was changed externally; reload before saving",
    "path": "posts/2026-01-10-hello/index.md",
    "current_revision": "sha256:..."
}
```

### POST /admin/api/create

新規記事を作成します。

**リクエストボディ**:
```json
{
    "collection": "posts",
    "fields": {
        "title": "New Article",
        "slug": "new-article"
    }
}
```

**レスポンス**:
```json
{
    "status": "created",
    "path": "posts/20260110-new-article/index.md",
    "revision": "sha256:..."
}
```

**エラーレスポンス** (既に存在する場合):
```json
{
    "status": "error",
    "code": "CONFLICT",
    "message": "File already exists"
}
```

### POST /admin/api/delete

記事を削除します。

**リクエストボディ**:
```json
{
    "path": "posts/2026-01-10-hello/index.md",
    "base_revision": "sha256:..."
}
```

`base_revision`が現在のrevisionと一致しない場合、記事は削除せずHTTP 409を返します。

**レスポンス**:
```json
{
    "status": "deleted",
    "local_preview_sync": true
}
```

記事削除はproductionの削除結果を正とし、Local Live Previewの事前metadata invalidationはstale URL防止のために試行します。Previewが停止中またはmetadata invalidation・shadow同期に失敗した場合も記事削除自体は成功し、`local_preview_sync`が`false`になります。`false`の場合はserver logとPreviewの再起動・再同期を確認してください。

### POST /admin/api/diff

記事の差分を取得します。

**クエリパラメータ**:
- `path` (必須): 記事のパス

**リクエストボディ** (オプション):
```json
{
    "content": "現在のエディタ内容"
}
```

**レスポンス**:
```json
{
    "diff": "--- HEAD\n+++ Current\n@@ -1,3 +1,3 @@\n...",
    "type": "git"  // "git", "unsaved", or "none"
}
```

---

## サイトAPI

### GET /admin/api/sites

読み込まれたSite Registryを取得します。

**レスポンス**:

```json
{
  "default_site": "techblog",
  "selected_site": "techblog",
  "sites": [
    {
      "id": "techblog",
      "name": "Tech Blog",
      "repo_path": "D:/sites/techblog",
      "generator": "hugo",
      "content_dir": "content",
      "static_dir": "static",
      "public_dir": "public",
      "preview": {
        "markdown": {"enabled": true},
        "deployment": {
          "provider": "cloudflare_pages",
          "access_protected": true,
          "cloudflare_pages": {"account_id": "...", "project_name": "techblog"}
        }
      },
      "snippet_paths": ["D:/sites/techblog/.vscode/md.code-snippets"]
    }
  ]
}
```

---

## Preview

### GET /admin/api/preview/local/status

対象siteのLocal Live Preview process、shadow workspace、常駐supervisorを返します。`always_on`が有効なsiteでは`next_refresh_at`と`refresh_timezone`がdaily refreshの実効値として含まれます。

```json
{
  "enabled": true,
  "status": "ready",
  "process_state": "ready",
  "workspace_active": false,
  "workspace_rebuild_required": false,
  "always_on": true,
  "supervisor_state": "running",
  "next_refresh_at": "2026-09-13T04:00:00+09:00",
  "refresh_timezone": "Asia/Tokyo"
}
```

`supervisor_state`は`starting`、`running`、`retrying`、`stopped`、`disabled`のいずれかです。scheduled refreshはworkspaceをdetach/deleteせず、generator processだけをgraceful restartします。

Preview同期に失敗してworkspaceの再構築が必要な場合は`workspace_rebuild_required: true`になります。次回のPreviewアクセスまたは常駐supervisorの復旧で、generatorとshadow workspaceをproduction treeから再生成し、成功時にこの状態を解除します。

### POST /admin/api/preview/markdown

編集中のMarkdownを安全なHTMLへ変換します。保存やremote buildは行いません。raw HTMLは無効化され、sanitize後のfragmentだけを返します。最大本文サイズは1 MiBです。

**リクエスト**:

```json
{
  "path": "posts/hello/index.md",
  "body": "# Hello\n\n![image](image.png)",
  "frontmatter": {"title": "Hello", "draft": true}
}
```

**レスポンス**:

```json
{"html": "<dl class=\"markdown-preview-frontmatter\">...</dl><h1>Hello</h1>..."}
```

relative imageは記事bundle、root-relative imageはサイトの`static_dir`から解決し、許可済みmediaを`/admin/api/media/raw`経由で表示します。外部HTTP(S)画像以外のschemeは拒否します。

### POST /admin/api/preview/deployments

保存済みworking treeからdraft branchをcommit/pushし、deployment追跡を開始します。remote変更を行うのはこの明示APIだけです。

```json
{"path":"posts/hello/index.md","draft_id":"550e8400-e29b-41d4-a716-446655440000"}
```

レスポンスは次のstateです。

```json
{
  "draft_id": "550e8400-e29b-41d4-a716-446655440000",
  "article_path": "posts/hello/index.md",
  "branch": "cms-preview/550e8400-e29b-41d4-a716-446655440000",
  "commit_sha": "0123456789abcdef...",
  "deployment_id": "...",
  "status": "queued",
  "url": "",
  "created_at": "2026-08-01T12:00:00Z",
  "updated_at": "2026-08-01T12:00:00Z"
}
```

### GET /admin/api/preview/deployments/:draft_id

providerを照会し、`queued`、`building`、`ready`、`failed`のstateを返します。`ready`の`url`はレスポンスの`commit_sha`と一致するdeployment固有URLです。

### POST /admin/api/preview/deployments/:draft_id/retry

failed deploymentを明示的に再試行します。

### POST /admin/api/preview/deployments/:draft_id/discard

provider deploymentとremote draft branchをcleanupします。失敗時は再試行できるstateを残します。

旧`/admin/preview/:site/*` proxy、`POST /admin/api/build`、`POST /admin/api/build/restart`は廃止されました。

---

## Git API

### POST /admin/api/sync

リモートリポジトリから最新の変更を取得します。

**レスポンス**:
```json
{
    "status": "ok",
    "log": "Already up to date.",
    "local_preview_reset": true
}
```

Git Sync成功後は、対象siteのLocal Live Preview processとshadow workspaceを既存のcleanup gateで停止・detachします。通常siteは次回Preview利用時に、更新後のproduction repository treeからlazy startします。`always_on: true`のsiteは手動停止を解除し、更新後のproduction repository treeからbackground supervisorが再度prewarmします。`local_preview_reset`が`false`の場合もGit Sync自体は成功しており、`local_preview_reset_error`とserver logでPreview側の失敗を確認できます。

### POST /admin/api/publish

readyになったdraft branchからproduction branchへのPull Requestを作成します。production branchへ直接pushしません。requestの`path`はpreview stateへ保存されたarticle pathと一致する必要があり、異なる場合はHTTP 409を返します。working tree、remote branch、PR headがpreview済みcommitと一致しない場合もHTTP 409となり、デプロイプレビューの更新が必要です。

**リクエストボディ**:
```json
{
    "path": "posts/2026-01-10-hello/index.md",
    "draft_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

**レスポンス**:
```json
{
    "status": "ok",
    "url": "https://github.com/owner/repository/pull/123"
}
```

---

## メディアAPI

### GET /admin/api/media

メディアファイル一覧を取得します。

**クエリパラメータ**:
- `mode`: `static` または `content`
- `path` (modeがcontentの場合): 記事のパス

**レスポンス**:
```json
[
    {
        "name": "image.jpg",
        "path": "/images/image.jpg",
        "size": 102400,
        "url": "/admin/api/media/raw?path=static/images/image.jpg",
        "repo_path": "static/images/image.jpg"
    }
]
```

### POST /admin/api/media

メディアファイルをアップロードします。

**Content-Type**: `multipart/form-data`

**フォームフィールド**:
- `file` (必須): アップロードするファイル
- `mode`: `static` または `content`
- `path` (modeがcontentの場合): 記事のパス

**制限**:
- 最大サイズ: `MAX_UPLOAD_SIZE_MB` (デフォルト10MB)
- 許可される拡張子: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.mp4`, `.webm`, `.pdf`
- 拡張子と実際のContent-Typeが一致しないファイル、および実行可能コンテンツを含められるSVGは拒否

**レスポンス**:
```json
{
    "name": "image_1704844800.jpg",
    "path": "/images/image_1704844800.jpg",
    "size": 102400,
    "url": "/admin/api/media/raw?path=static/images/image_1704844800.jpg",
    "repo_path": "static/images/image_1704844800.jpg",
    "local_preview_sync": true
}
```

`local_preview_sync`はproductionへの保存後に行うLocal Live Previewのshadow同期・metadata invalidationの結果です。Preview側の障害だけでアップロードをHTTP 500にはせず、同期に失敗した場合は`false`を返します。

**エラーレスポンス**:
```json
{
    "status": "error",
    "code": "BAD_REQUEST",
    "message": "File too large. Maximum size is 10MB"
}
```

### POST /admin/api/media/delete

メディアファイルを削除します。

**リクエストボディ**:
```json
{
    "repo_path": "content/posts/20260608/images/image.jpg",
    "article_path": "posts/20260608/takao.md"
}
```

**制限**:
- `static/` または `content/` 内のファイルのみ削除可能
- Article Mediaでは `article_path` を指定し、collectionの `media_folder` で解決された対象ディレクトリ内のファイルだけ削除可能

**レスポンス**:
```json
{
    "status": "deleted",
    "local_preview_sync": true
}
```

削除もproductionの結果を正とします。Preview同期が失敗した場合は削除を再試行せず、`local_preview_sync: false`を診断情報として扱います。

### GET /admin/api/media/raw

メディアファイルを直接取得します (プレビュー用)。

**クエリパラメータ**:
- `path` (必須): ファイルのリポジトリ相対パス

**レスポンス**: ファイルの内容 (適切なContent-Type)

---

## 設定API

### GET /admin/api/config

CMS設定を取得します。サイトリポジトリ直下の `.homecms.yml` がある場合はそれを優先し、存在しない場合は既存互換の `<static_dir>/admin/config.yml` を読み込みます。

**レスポンス**:
```json
{
    "collections": [
        {
            "name": "posts",
            "label": "ブログ記事",
            "folder": "content/posts",
            "media_folder": "{{dirname}}/images",
            "create": true,
            "fields": [...]
        }
    ],
    "media_folder": "/static/images",
    "public_folder": "/images",
    "_cms": {
        "site_id": "default",
        "content_dir": "content",
        "static_dir": "static",
        "public_dir": "public",
        "article_media_dir": "images",
        "site_generator": "hugo",
        "config_source": ".homecms.yml",
        "markdown_preview": {"enabled": true},
        "preview_deployment": {
            "enabled": true,
            "provider": "cloudflare_pages",
            "access_protected": true
        },
        "warnings": [
            {
                "severity": "warning",
                "code": "unknown_preview_url_field",
                "path": "preview.url_field",
                "message": "preview.url_field \"permalink\" is not defined in any collection fields; previews may fall back to file paths."
            }
        ]
    }
}
```

`_cms.warnings`は設定上の注意点がない場合は空配列です。`severity`は`warning`または`error`で、parse不能な設定は従来通りAPIエラーになります。

---

## ヘルスチェックAPI

### GET /health

基本的なヘルスステータスを返します (認証不要)。

**レスポンス**:
```json
{
    "status": "ok",
    "timestamp": "2026-01-10T12:00:00Z",
    "uptime": "1h30m45s",
    "version": "1.0.0"
}
```

### GET /ready

詳細なヘルスチェックを実行します (認証不要)。

**レスポンス (正常時)**:
```json
{
    "status": "ok",
    "timestamp": "2026-01-10T12:00:00Z",
    "uptime": "1h30m45s",
    "sites": {
        "tech": {
            "healthy": true,
            "checks": {
                "content_dir": {"healthy": true},
                "git_repo": {"healthy": true}
            }
        }
    }
}
```

single-site構成では互換性のため、同じsiteの`checks`もレスポンス直下に含まれます。`sites`には全siteのrepository/content依存関係が含まれ、いずれかがunhealthyならHTTP 503になります。Local Live Previewの停止・再試行状態はoptional dependencyのため、readiness判定には含めません。

**レスポンス (異常時)**: HTTP 503
```json
{
    "status": "degraded",
    "sites": {
        "daily": {
            "healthy": false,
            "checks": {"content_dir": {"healthy": false}, "git_repo": {"healthy": true}}
        }
    }
}
```

---

## エラーコード

| コード | HTTPステータス | 説明 |
|--------|---------------|------|
| `UNAUTHORIZED` | 401 | 認証が必要 |
| `FORBIDDEN` | 403 | アクセス拒否 |
| `BAD_REQUEST` | 400 | 不正なリクエスト |
| `NOT_FOUND` | 404 | リソースが見つからない |
| `CONFLICT` | 409 | リソースが既に存在 |
| `INTERNAL_ERROR` | 500 | サーバー内部エラー |
| `INVALID_CSRF` | 403 | CSRFトークンが無効 |
| `USER_NOT_ALLOWED` | 403 | ユーザーが許可リストにない |

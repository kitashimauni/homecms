# 現行アーキテクチャ

> PreviewについてはIssue #30でローカルgenerator process/proxyを廃止したため、[本文プレビューとデプロイプレビュー](preview-deployment-design.md)を正とします。本書に残るHugo preview serverの記述は旧構成です。

HomeCMSのシステム構成と実装詳細について説明します。

## システム概要

```
┌─────────────────────────────────────────────────────────────┐
│                        Browser                               │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
│  │  app.js  │  │  api.js  │  │editor.js │  │  ui.js   │    │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘    │
└───────┼─────────────┼─────────────┼─────────────┼──────────┘
        │             │             │             │
        ▼             ▼             ▼             ▼
┌─────────────────────────────────────────────────────────────┐
│                     Gin HTTP Server                          │
│  ┌─────────────────────────────────────────────────────┐    │
│  │                    Middleware                        │    │
│  │  Session │ CSRF │ Auth │ TokenValidation            │    │
│  └─────────────────────────────────────────────────────┘    │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐              │
│  │   api.go   │ │  auth.go   │ │  media.go  │              │
│  │  Articles  │ │   OAuth    │ │   Upload   │              │
│  └─────┬──────┘ └─────┬──────┘ └─────┬──────┘              │
└────────┼──────────────┼──────────────┼─────────────────────┘
         │              │              │
         ▼              ▼              ▼
┌─────────────────────────────────────────────────────────────┐
│                       Services                               │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       │
│  │ cache.go │ │  git.go  │ │generator │ │ media.go │       │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘       │
└───────┼────────────┼────────────┼────────────┼──────────────┘
        │            │            │            │
        ▼            ▼            ▼            ▼
┌─────────────────────────────────────────────────────────────┐
│                    External Systems                          │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       │
│  │   File   │ │   Git    │ │   Hugo   │ │  GitHub  │       │
│  │  System  │ │   CLI    │ │  Server  │ │   API    │       │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘       │
└─────────────────────────────────────────────────────────────┘
```

## コンポーネント詳細

### main.go - エントリーポイント

アプリケーションの起動と設定を担当:

1. 設定の初期化 (`config.Init()`)
2. default siteのpreview server起動
3. Ginルーターの設定
4. ミドルウェアの適用
5. グレースフルシャットダウン

```go
// ルーティング構成
/admin              # CMS UI
/admin/auth/*       # 認証 (OAuth)
/admin/api/*        # REST API (認証必須)
/admin/preview/:site/* # サイト別プレビュー (認証付きプロキシ)
/health             # ヘルスチェック
/ready              # 詳細ヘルスチェック
```

### pkg/handlers/ - HTTPハンドラー

#### auth.go

- **AuthRequired**: セッション認証ミドルウェア
- **CSRFProtection**: CSRFトークン検証
- **TokenValidation**: GitHubトークンの定期検証
- **GithubLogin / AuthCallback**: OAuth フロー

#### api.go

- **ListArticles**: 記事一覧 (キャッシュ使用)
- **GetArticle / SaveArticle**: 記事の取得・保存
- **CreateArticle / DeleteArticle**: 記事の作成・削除
- **HandleSync / HandlePublish**: Git操作

#### media.go

- **ListMedia**: メディアファイル一覧
- **UploadMedia**: ファイルアップロード (サイズ制限あり)
- **DeleteMedia**: ファイル削除 (パス検証あり)
- **ServeMediaRaw**: 直接ファイル配信

#### errors.go

統一されたエラーレスポンス形式:

```json
{
  "status": "error",
  "code": "BAD_REQUEST",
  "message": "詳細なエラーメッセージ"
}
```

### pkg/services/ - ビジネスロジック

#### cache.go - 記事キャッシュ

並列処理による高速な記事読み込み:

```go
// 20並列でファイルを読み込み
semaphore := make(chan struct{}, config.CacheConcurrency)
```

- Front Matterの先頭部分のみ読み込み (`FileReadHeadLimit`)
- Gitステータスの取得 (dirty判定)
- キャッシュの自動無効化

#### git.go - Git操作

GitHub Personal Access Token (OAuth) を使用:

```go
// ASKPASS方式による認証
// トークンを環境変数経由で渡し、コマンドライン履歴に残らない
GIT_ASKPASS=script.sh
GIT_TOKEN=xxx
```

タイムアウト設定:
- ローカル操作: 60秒
- ネットワーク操作: 5分

保存、同期、公開はリポジトリ操作ロックで直列化される。公開処理はステージ済み変更を確認し、commitが成功した場合だけpushする。

#### generator.go / hugo_adapter.go / eleventy_adapter.go - ジェネレーター管理

`GeneratorAdapter`がプレビュー起動・停止・再起動、ビルド、コンテンツ作成を抽象化する。`SITE_GENERATOR`またはSite Registryのdefault site設定から`HugoAdapter`または`EleventyAdapter`を選択する。従来の関数名は互換ラッパーとして維持している。

各Adapterはpreview serverを`SiteRuntime.PreviewURL`へproxyする。Hugoは`--baseURL`を使い、Eleventyはdev serverが返すroot-relative URLを使う。HTTP proxyは認証付きpreviewのpathとそのencoding/queryを維持したまま上流へ転送する。

`ProcessManager`はプロセス終了を明示的に待ち、世代の異なる監視処理が新しいプロセス状態を消去しないよう管理する。

Hugo/Eleventyなどのサイトジェネレーター子プロセスにはallowlist化した環境変数だけを渡し、CMSのセッション秘密鍵やGitHub OAuth secretを継承させない。

### Docker実行境界

Docker構成はappの可用性とサイト実行コードの準備を分離する。

- `hugo-cms` serviceはbuild ARGで作成した非root UID/GIDでCMSを実行する
- app起動時に`mise install`、Node.js依存取得、bind mountの`chown`は行わない
- host portは`127.0.0.1:${HOMECMS_HOST_PORT:-8080}`へだけ公開し、container内`PORT`は`8080`に固定する
- repoはhostから`/data/repos`へbind mountし、mise tools/cacheは`mise-data` named volumeに保持する
- `tool-bootstrap`は`tools` profileのone-shot serviceで、appのsecret環境変数を受け取らない
- bootstrap対象は`HOMECMS_REPOS`へUnixの`:`区切りで明示したrepoだけであり、`/data/repos/*`は自動実行しない

operatorはサイトのmise設定、package metadata、lockfile、install scriptをレビューしてからbootstrapを実行する。toolchainや依存を更新した場合はone-shotを再実行するが、通常のapp再起動は準備済み環境を変更しない。

この分離はbootstrapへCMS secretを直接渡さないための初期境界であり、サイト生成processは現在もappと同じcontainer内で動く。サイト単位のcontainer分離、リソース制限、別origin previewは未実装である。

#### frontmatter_codec.go - Front Matter codec

YAML、TOML、JSONを共通インターフェースで解析・生成する。JSON Front Matterの終了位置以降をMarkdown本文として保持し、コレクションの`format`指定を新規記事へ反映する。

#### frontmatter.go - Front Matter処理

サポートフォーマット:
- YAML (`---`)
- TOML (`+++`)
- JSON (`{}`)

正規化処理:
- 日付フォーマットの統一
- 空フィールドの除去
- デフォルト値の適用

### pkg/config/ - 設定管理

環境変数の読み込みと設定の管理:

```go
var (
    RepoPath          = "./repo"
    ContentDir        = "content"
    StaticDir         = "static"
    PublicDir         = "public"
    SiteGenerator     = "hugo"
    MaxUploadSize     = int64(10 * 1024 * 1024)
    GitCommandTimeout = 60 * time.Second
    // ...
)
```

`SITES_CONFIG_PATH`を指定するとSite Registryを読み込み、default siteの設定を既存APIに適用する。読み込み結果は`GET /admin/api/sites`で確認できる。

Site RegistryはYAMLの未知フィールドを拒否し、site IDの重複、必須値の欠落、未対応のgenerator/runtime、リポジトリ外のdirectory設定を起動時にfail fastで検出する。環境変数由来のlegacy single-site設定は従来のdefault補正を維持する。

### pkg/models/ - データモデル

#### Article

```go
type Article struct {
    Path        string                 // content相対パス
    Title       string                 // タイトル
    Date        time.Time              // 日付
    Draft       bool                   // 下書きフラグ
    Dirty       bool                   // 未公開変更あり
    FrontMatter map[string]interface{} // 全Front Matter
}
```

#### CMSConfig

```go
type CMSConfig struct {
    Collections []Collection
}

type Collection struct {
    Name        string
    Label       string
    Folder      string
    Create      bool
    Slug        string
    Path        string
    Format      string
    Fields      []Field
    MediaFolder string
    // ...
}
```

## データフロー

### 記事の読み込み

```
1. クライアント: GET /admin/api/articles
2. ListArticles() → GetArticlesCache()
3. キャッシュあり → 即座に返却
4. キャッシュなし →
   a. content/ を走査
   b. 並列でFront Matter読み込み
   c. Gitステータス取得
   d. キャッシュに格納
5. JSON応答
```

### 記事の保存

```
1. クライアント: POST /admin/api/article
2. CSRFトークン検証
3. SaveArticle() →
   a. パス検証 (SafeJoin)
   b. Front Matter正規化
   c. repository lock内で現在のcontent revisionを検証
   d. revisionが一致しなければ409 Conflict
   e. ファイル書き込み
   f. 新revisionを返してキャッシュ更新
4. 成功応答（新revision）
```

### 記事の公開

```
1. クライアント: POST /admin/api/publish
2. CSRFトークン検証
3. HandlePublish() → PublishChanges()
   a. git config (user.name, user.email)
   b. git add
   c. git commit
   d. git push (ASKPASS認証)
4. キャッシュ無効化
5. 成功応答 (Gitログ)
```

## セキュリティ実装

### 認証フロー

```
1. /admin/auth/login
2. GitHub OAuth認証画面へリダイレクト
3. GitHub がcallback URLへリダイレクト
4. /admin/auth/callback
   a. stateパラメータ検証
   b. アクセストークン取得
   c. GitHubユーザー情報取得
   d. ユーザー許可リスト確認
   e. セッションにトークン保存
5. /admin へリダイレクト
```

`TokenValidation`は5分ごとにGitHub API `/user`を再検証する。`200`は検証時刻を更新し、`401`だけを明確なtoken invalidとしてsessionを失効する。`403`、`429`、`5xx`、timeout/DNS/TLSなどのtransport failureは`unavailable`としてwarningを記録し、検証時刻を更新せず、`token_validation_retry_at`による60秒のbackoff後に再試行する。backoff中は各requestでGitHub APIを呼び出さず、GitHubの一時障害だけでユーザーをlogoutしない。

### CSRF保護

```
1. ログイン成功時にCSRFトークン生成
2. セッションに保存
3. クライアントは /admin/api/csrf からトークン取得
4. POST/PUT/DELETE時に X-CSRF-Token ヘッダーで送信
5. サーバーでセッションのトークンと照合
```

### パストラバーサル対策

```go
func SafeJoin(root, sub, target string) string {
    // 絶対パスを拒否
    if filepath.IsAbs(target) {
        return ""
    }
    
    finalPath := filepath.Join(root, sub, target)
    
    // 相対パスで確認
    rel, _ := filepath.Rel(filepath.Join(root, sub), finalPath)
    if strings.HasPrefix(rel, "..") {
        return ""  // 親ディレクトリへの参照を拒否
    }
    
    return finalPath
}
```

## 並行処理

### キャッシュの排他制御

article cacheのmapを守るglobal lockと、各`repo_path + content_dir` keyのrebuild/update lockを分離する。同一keyのrebuildは一つに集約し、WalkDir・Git status・file readなどの高コストI/O中にglobal lockを保持しないため、別siteのcache accessをblockしない。

### Repository操作の排他制御

`LockRepositoryOperation(runtime)`は正規化したrepository pathごとのlock registryを使用する。同一repositoryを参照するsiteはsave/delete、media、Git Sync、publish、draft previewを引き続き直列化し、異なるrepositoryの操作は並行して進められる。lock entryは参照がなくなった時点でregistryから削除する。

### Hugoサーバーの排他制御

```go
var hugoServerMu sync.Mutex

func StartHugoServer() error {
    hugoServerMu.Lock()
    defer hugoServerMu.Unlock()
    // ...
}
```

## エラーハンドリング

### タイムアウト

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

cmd := exec.CommandContext(ctx, "hugo", "build")
output, err := cmd.CombinedOutput()

if ctx.Err() == context.DeadlineExceeded {
    return "", fmt.Errorf("operation timed out")
}
```

### グレースフルシャットダウン

```go
quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
<-quit

// Hugoサーバー停止
services.StopHugoServer()

// HTTPサーバーシャットダウン (5秒タイムアウト)
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
srv.Shutdown(ctx)
```

## ロギング

`log/slog` による構造化ログ:

```go
slog.Info("Hugo build completed", 
    "duration", time.Since(start))

slog.Warn("Failed to set git user.email", 
    "error", err)

slog.Debug("Git command executed", 
    "args", args, 
    "duration", time.Since(start))
```

## テスト

### ユニットテスト

```bash
go test ./pkg/... -v
```

テストカバレッジ:
- `pkg/handlers/`: エラーレスポンス、ヘルスチェック、メディアAPI
- `pkg/services/`: SafeJoin、Front Matter、ファイル操作

### 統合テスト

実際のGit/Hugoコマンドを使用するため、CI環境では注意が必要です。

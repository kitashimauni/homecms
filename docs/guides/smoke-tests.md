# Smoke Test Checklist

最終更新日: 2026-08-01

大きめの変更やSite Registry変更後に、最低限確認する項目です。

リリース候補PRでは、ここに加えて[リリース前チェックリスト](release-checklist.md)も確認してください。

## 1. 共通

```powershell
go test ./...
go vet ./...
go build -buildvcs=false ./...
npm run test:js
git diff --check
```

miseを使う場合は、同じ検証をまとめて実行できます。

```powershell
mise run check
```

`mise run check`はGoのtest/vet/buildに加えて、Node標準テストランナーによるフロントエンドの軽量テストも実行します。

ブラウザでは以下を確認します。

- `/admin/login` からログインできる
- `/health` が `200`
- `/ready` がdefault siteの状態を返す
- `/admin/api/sites` がSite Registryを返す
- スマホで記事末尾までスクロールでき、下部ブラウザUIやsafe-areaと本文が重ならない
- スマホでソフトウェアキーボードを表示しても、記事末尾の行へカーソルを移動して編集できる

## 2. 単一Hugoサイト

`SITES_CONFIG_PATH`を未設定にして確認します。

- 記事一覧が表示される
- 記事を開ける
- front matter formが表示される
- 保存できる
- Diffが表示される
- 画像メディア一覧が表示される
- スニペットが `<REPO_PATH>/.vscode/md.code-snippets` から読み込まれる
- 本文プレビューが保存前のMarkdown入力へ追従する
- raw HTML、`javascript:` link、event handlerが本文プレビューへ残らない
- 見出し、list、引用、code block、table、link、Markdown imageが表示される
- 空本文と1 MiB超の本文が予期した状態/エラーになる

## 3. 複数サイト

例:

```yaml
default_site: techblog
sites:
  - id: techblog
    name: Tech Blog
    repo_path: D:/sites/techblog
    generator: hugo
    content_dir: content
    static_dir: static
    public_dir: public
    preview:
      markdown:
        enabled: true
      deployment:
        provider: cloudflare_pages
        access_protected: true
        cloudflare_pages:
          account_id: account-id
          project_name: techblog
          token_env: TECHBLOG_CLOUDFLARE_API_TOKEN

  - id: notes
    name: Notes
    repo_path: D:/sites/notes
    generator: eleventy
    content_dir: src
    static_dir: public-assets
    public_dir: _site
```

確認項目:

- サイドバーのSite selectorで切り替えられる
- 切り替え後、記事一覧が選択サイトの内容になる
- `/admin/api/articles?site=<id>` が対象サイトの内容を返す
- `/admin/api/config?site=<id>` の `_cms.site_id` が対象サイトになる
- `.homecms.yml`があるサイトでは`/admin/api/config?site=<id>` の `_cms.config_source` が `.homecms.yml` になる
- 設定に注意点がある場合、`/admin/api/config?site=<id>` の `_cms.warnings` と管理画面サイドバーに表示される
- legacy `static/admin/config.yml` を使うサイトでは互換読み込みwarningが表示される
- 記事の作成、保存、Diff、削除が選択サイトの`content_dir`配下だけを対象にする
- メディア一覧、アップロード、削除、raw配信が選択サイトの`static_dir`/記事別メディア設定だけを対象にする
- `static_media_dir`未指定かつ`.homecms.yml`の`media.folder`があるサイトでは、そのフォルダへstatic media uploadされ、`media.public_path`がMarkdown挿入パスに使われる
- スニペットが選択サイトの`snippet_paths`または`<repo_path>/.vscode/md.code-snippets`から読み込まれる
- 本文プレビューのrelative imageが選択siteの記事bundleだけから解決される
- root-relative imageが選択siteの`static_dir`だけから解決される
- provider未設定siteでも編集・保存・本文プレビューが動く
- provider設定siteだけにデプロイプレビュー操作が表示される
- Access未保護設定ではpublic preview警告が表示される

## 4. Eleventy

対象リポジトリには以下が必要です。

- `package.json`
- `@11ty/eleventy` dependency
- lockfile (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lock`, `bun.lockb`)
- `content_dir` と `public_dir` の明示

確認項目:

- 記事一覧が`content_dir`配下から取得される
- 記事を保存できる
- Buildがlockfileに対応するpackage manager経由で実行される
- Dockerでは`tool-bootstrap`がlockfileに対応するfrozen installを完了してから明示Buildを実行できる
- Local Live Previewの`--json` URL解決と`--serve` LiveReloadが、実際のEleventy 3.xで動作する
- cleanなproduction outputからのpreviewで、`config.dir`、collection glob、passthrough、plugin/shortcodeの追加出力が動作する
- Local Live Previewの生成物がproduction repositoryの`public_dir`を変更しない

## 5. Docker

- `.env`がない場合はComposeがapp起動前に明確に失敗する
- appと`tool-bootstrap`のUID/GIDが非rootで、app起動によってhost repoの所有者が変わらない
- host GIDがbase image内の既存groupと重複してもimageをbuildでき、指定した数値UID/GIDで動作する
- UID/GIDに`0`または`00`などのゼロ埋め表現を指定するとimage buildが失敗する
- host portが`127.0.0.1:${HOMECMS_HOST_PORT:-8080}`だけに公開され、container内`PORT`が`8080`
- `mise-data`がnamed volumeで、app再作成後も準備済みtoolchainを利用できる
- app起動・再起動では`mise install`やNode.js依存installが実行されない
- `docker compose --profile tools run --rm tool-bootstrap`が`HOMECMS_REPOS`のUnix `:`区切りallowlistだけを処理する
- allowlist外の`/data/repos`配下repoはbootstrapされない
- `tool-bootstrap`に`GITHUB_CLIENT_SECRET`、`SESSION_SECRET`などのapp secretが存在しない
- mise設定とlockfileを更新してbootstrapを再実行すると、準備済み環境が安全に更新される
- bootstrap失敗時も既に起動しているappは停止・再起動されない

## 6. Git・デプロイプレビュー操作

- Syncが対象サイトのrepoで実行される
- autosaveと本文プレビューではcommit/pushされない
- 「デプロイプレビューを更新」でだけ`cms-preview/<draft-id>`へcommit/pushされる
- production worktreeのcurrent branch/indexが切り替わらない
- production branch更新後の再previewが最新production commitをparentにする
- draftごと、siteごとにbranch/stateが分離される
- 同じdraft IDを別記事pathへ流用するとHTTP 409になり、stateの対象pathsが検証に使われる
- publish中の同一draft更新が直列化され、remote branchとPR headがpreview済みcommitに一致しない場合はHTTP 409になる
- Cloudflareで`queued / building / ready / failed`がCMS状態へ正規化される
- ready URLとstateのcommit SHAが一致し、build中/失敗時は古いURLを表示しない
- retryとdiscard cleanupを再実行できる
- ready後のPublishがproduction branchへ直接pushせずPRを作成する

## 7. 注意点

- Cloudflare preview branchをbuild対象にし、API tokenを最小権限にしてください。
- `noindex`は認証ではありません。未公開contentにはCloudflare Accessを設定してください。
- 主要なsite-aware APIは`SiteRuntime`を明示的にserviceへ渡します。site間でcontent、media、draft branch、deployment stateが混ざらないことを重点的に確認してください。

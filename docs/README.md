# ドキュメント

HomeCMSの利用方法、仕様、設計、および監査結果を目的別に整理している。HugoやEleventyなどの静的サイトジェネレーター固有の説明は各ガイドに明記する。

## ガイド

- [設定ガイド](guides/configuration.md) - 環境変数と実行設定
- [Local Live Preview設定ガイド](guides/local-live-preview.md) - wildcard subdomain、Host validation、private ingress設定
- [デプロイガイド](guides/deployment.md) - サーバー、Docker、リバースプロキシ
- [Docker + mise デプロイガイド](guides/docker-mise-deployment.md) - secret-free one-shot bootstrap、非root app、サイト別toolchainの運用
- [`.homecms.yml` 移行ガイド](guides/migrating-to-homecms.md) - legacy `static/admin/config.yml` からの移行
- [リリース前チェックリスト](guides/release-checklist.md) - リリース候補PRで確認する項目
- [Smoke Test Checklist](guides/smoke-tests.md) - 変更後の最低限確認項目
- [テスト構成ガイド](guides/testing.md) - unit、handler contract、Local Preview lifecycle、外部 smoke の責務分担

## リファレンス

- [APIリファレンス](reference/api.md) - HTTP APIの仕様
- [CMS設定](reference/cms-config.md) - コレクション、フィールド、メディア設定
- [スニペット機能](reference/snippets.md) - VS Code形式スニペットの設定

## アーキテクチャ

- [現行アーキテクチャ](architecture/current-architecture.md) - 現在のgenerator共通実装とHugo/Eleventy adapter
- [マルチサイト・マルチジェネレーター設計](architecture/multi-site-generator-design.md) - 複数サイトとHugo/Eleventy等へ対応するための提案
- [本文プレビューとデプロイプレビュー](architecture/preview-deployment-design.md) - preview責務、draft branch、provider、security、cleanup
- [Local Live Preview設計](architecture/local-live-preview-design.md) - wildcard hostname、process lifecycle、port reservation、shadow workspace
- [ADR-0001: Cloudflare Pages preview](architecture/adr-0001-cloudflare-pages-preview.md) - 初期providerの選定理由

## 監査

- [セキュリティ・品質監査](audits/security-and-quality-audit.md) - 既知の問題、推奨対応、mise導入方針

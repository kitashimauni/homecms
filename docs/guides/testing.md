# テスト構成ガイド

テストは責務ごとに分け、Local Preview の process、workspace、supervisor の境界をまたぐ fixture は共有しない。共有するのは、各テスト層で繰り返す最小限の harness に限る。

## テスト層

| 層 | 主な対象 | 実行方法 |
| --- | --- | --- |
| Pure / unit | 設定値の検証、URL解決、revision、状態遷移、parser | `go test ./pkg/config ./pkg/services` |
| HTTP handler contract | Gin handler の status、JSON、site query、selected runtime | `go test ./pkg/handlers` |
| Local Preview lifecycle | fake command を使う process、workspace、supervisor の起動・停止・retry・cleanup | `go test ./pkg/services -run LocalPreview` |
| Generator integration | Hugo / Eleventy の実コマンド、metadata、preview URL | `go test ./...`、`npm run test:js` |
| Docker / Compose smoke | 非root runtime、loopback公開、allowlist、bootstrap | [Smoke Test Checklist](smoke-tests.md) |
| External ingress smoke | 実サイト、wildcard ingress、viewer authentication（Issue #37） | 環境依存の受入確認。通常のCIには含めない |

Local Preview の共通 test harness は `pkg/services/local_preview_test_helpers_test.go` に置く。`SiteRuntime`、一時 repo、shadow workspace、fake process をまとめて用意するが、generator 固有の引数や process-tree 検証は各テストファイルに残す。

## 標準チェック

変更時は次を確認する。

```text
go test ./...
go vet ./...
go test -race ./...
npm run test:js
```

`go test -race ./...` はLinux CIで実行し、Windows固有のprocess-tree挙動はWindows CIまたは実機で確認する。Issue #37 の実サイト smoke は、必要なDNS・証明書・認証環境を用意した上で別途実施する。

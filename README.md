# Jipcy (PoC)
自然言語のクエリから関連するJiraの課題を検索し、Slack上のスレッド内容なども考慮して類似度を算出し、その課題の概要をサマリ生成するCLIツールです。
PoC（概念実証）段階のため、セットアップ手順は省略しています。

## 機能概要
1. 自然言語によるクエリ入力

コマンド実行時に自然言語で問い合わせ内容（クエリ）を指定すると、OpenAI / Azure OpenAI API を利用して Jira 検索用のクエリ（JQL）を生成します。

2. Jira 課題検索 & 類似度評価

生成した JQL を用いて Jira API から課題を取得します。
取得した課題について、OpenAI / Azure OpenAI API のエンベディングや ChatCompletion を利用し、入力されたクエリとの類似度を算出します。

3. Slack スレッドの関連情報取得（オプション）

Slack API Token が設定されている場合は、Jira チケットの URL をキーワードとして Slack 内のスレッドを検索し、内容を取得します。

4. サマリ生成

各 Jira 課題（および関連する Slack スレッド）をもとに、OpenAI / Azure OpenAI API を利用してサマリを生成します。
ユーザが新しい課題を立てる必要があるかどうかを素早く判断するために、課題内容と解決方法が簡潔にまとめられます。

5. 結果の表示

類似度の高い上位 3 件（既定）を表示し、生成したサマリを確認できます。

## 必要な環境変数
本ツールは以下の環境変数を利用します。少なくとも Jira 関連の環境変数は必須です。

### OpenAI または Azure OpenAI のクレデンシャル
- OPENAI_API_KEY

OpenAI API Key。これが設定されている場合は OpenAI を使用します。
Azure OpenAI を使用したい場合は本変数を設定しないでください。

- AZURE_OPENAI_KEY

Azure OpenAI で使用する API Key。
Azure を使用する場合は、AZURE_OPENAI_ENDPOINT と合わせて設定してください。

- AZURE_OPENAI_ENDPOINT

Azure OpenAI のエンドポイント URL。例: https://<your-resource-name>.openai.azure.com
Azure を使用する場合は必須です。

- AZURE_OPENAI_API_VERSION

Azure OpenAI の API バージョン。指定がない場合は 2025-01-01-preview が使用されます。

- OPENAI_MODEL

OpenAI あるいは Azure OpenAI が使用するモデル名。
ChatCompletion 形式で動作するモデル名を指定してください（例: gpt-3.5-turbo）。

### Jira のクレデンシャル
- JIRA_USERNAME

Jira ログインに使用するユーザ名（メールアドレスなど）。

- JIRA_API_TOKEN

Jira の API トークン。

- JIRA_ENDPOINT

Jira のエンドポイント。例: https://<your-domain>.atlassian.net

- JIRA_PROJECT_KEY

使用する Jira プロジェクトのキー。例: ABC

### Slack のクレデンシャル（オプション）
- SLACK_API_TOKEN

Slack の API Token。
指定がない場合は Slack スレッド検索は行われません。

## 実行方法 (PoC 版)
PoC のため、詳細なセットアップ方法やインストール手順は省略します。
.env ファイルなどに上記の環境変数を設定し、以下のように実行すると、自然言語で指定したクエリに関連する Jira 課題を検索し、類似度が高いものを表示します。

```go
go run main.go "〇〇についてのエラーが発生しているので対応方法が知りたい"
```

## ライセンス
MIT

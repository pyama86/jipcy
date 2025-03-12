# Jipcy

Jipcy は、Slack と Jira を統合し、自然言語で Jira の課題を検索・要約する Slack ボットです。

## 特徴
- **Slack メンションを受けると Jira の問い合わせを検索**
- **OpenAI を活用して Jira の検索クエリを自動生成**
- **Jira の検索結果を要約し、関連性の高い課題を Slack に通知**

## 必要な環境変数

Jipcy を動作させるために、以下の環境変数を設定してください。

```bash
SLACK_BOT_TOKEN=<Slack ボットの API トークン>
SLACK_APP_TOKEN=<Slack アプリレベルのトークン>
SLACK_USER_TOKEN=<Slack ユーザートークン>
SLACK_WORKSPACE_URL=<Slack のワークスペース URL>
JIRA_ENDPOINT=<Jira API のエンドポイント>
JIRA_USERNAME=<Jira のユーザー名>
JIRA_API_TOKEN=<Jira の API トークン>
JIRA_PROJECT_KEY=<Jira のプロジェクトキー>
```

## ライセンス
- MIT

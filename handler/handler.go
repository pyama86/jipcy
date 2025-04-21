package handler

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/andygrunwald/go-jira"
	"github.com/pyama86/jipcy/domain/infra"
	"github.com/pyama86/jipcy/domain/service"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/songmu/retry"
)

type Handler struct {
	slack       *infra.Slack
	jira        *infra.Jira
	openAI      *infra.OpenAI
	slackClient *slack.Client
	botID       string
}

func NewHandler(slack *infra.Slack, jira *infra.Jira, openAI *infra.OpenAI) *Handler {
	return &Handler{
		slack:  slack,
		jira:   jira,
		openAI: openAI,
	}
}

func (h *Handler) Handle() error {
	webApi := slack.New(
		os.Getenv("SLACK_BOT_TOKEN"),
		slack.OptionAppLevelToken(os.Getenv("SLACK_APP_TOKEN")),
	)
	socketMode := socketmode.New(
		webApi,
	)
	authTest, authTestErr := webApi.AuthTest()
	if authTestErr != nil {
		fmt.Fprintf(os.Stderr, "SLACK_BOT_TOKEN is invalid: %v\n", authTestErr)
		os.Exit(1)
	}
	h.botID = authTest.UserID
	h.slackClient = webApi
	go func() {
		for envelope := range socketMode.Events {
			switch envelope.Type {
			case socketmode.EventTypeEventsAPI:
				socketMode.Ack(*envelope.Request)
				eventPayload, ok := envelope.Data.(slackevents.EventsAPIEvent)
				if !ok {
					slog.Error("Failed to cast to EventsAPIEvent")
					continue
				}

				switch eventPayload.Type {
				case slackevents.CallbackEvent:
					innerEvent := eventPayload.InnerEvent
					switch ev := innerEvent.Data.(type) {
					case *slackevents.AppMentionEvent:
						h.handleMention(ev)
					default:
						socketMode.Debugf("Skipped: %v", envelope.Type)
					}
				}
			}
		}
	}()

	return socketMode.Run()
}

// エラー内容をポストする関数
func (h *Handler) postError(channelID, userID, message, ts string) {
	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "❌ エラー", false, false),
		),
		slack.NewDividerBlock(),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", message, false, false),
			nil, nil,
		),
	}
	if _, err := h.slackClient.PostEphemeral(
		channelID,
		userID,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionTS(ts),
	); err != nil {
		slog.Error("Failed to post message", slog.Any("err", err))
	}
}

// メンションを受け取ったときの処理
func (h *Handler) handleMention(event *slackevents.AppMentionEvent) {
	channelID := event.Channel
	userID := event.User

	// ボット自身のメンション (`@bot`) を削除
	messageText := strings.Replace(event.Text, fmt.Sprintf("<@%s>", h.botID), "", 1)
	messageText = strings.TrimSpace(messageText)

	if messageText == "" {
		h.postError(channelID, userID, "メッセージが空です。入力内容を確認してください。", event.TimeStamp)
		return
	}

	// 環境変数 SLACK_CHANNEL で指定されたチャンネル以外は応答しない
	if os.Getenv("SLACK_CHANNEL") != "" {
		allowedChannel := strings.TrimPrefix(os.Getenv("SLACK_CHANNEL"), "#")
		channelInfo, err := h.slack.GetChannelInfo(channelID)
		if err != nil {
			slog.Error("Failed to get channel info", slog.Any("err", err))
			return
		}

		if channelInfo.Name != allowedChannel {
			h.postError(channelID, userID, "このチャンネルでは応答しません。", event.TimeStamp)
			return
		}
		slog.Info("Allowed channel", slog.String("channel", channelInfo.Name))
	}

	var lastError error
	// 虚無に話しかけてるみたいになるのでメッセージを応答する
	if _, _, err := h.slackClient.PostMessage(
		channelID,
		slack.MsgOptionText(":white_check_mark: *お問い合わせを受け付けました！*\n以降はあなただけに返信します。:sparkles:", false),
		slack.MsgOptionTS(event.TimeStamp),
	); err != nil {
		slog.Error("Failed to post message", slog.Any("err", err))
		return
	}

	// 1. 処理開始の通知
	{
		blocks := []slack.Block{
			slack.NewHeaderBlock(
				slack.NewTextBlockObject("plain_text", "🚀 Jira問い合わせ開始", false, false),
			),
			slack.NewDividerBlock(),
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", "Jira問い合わせを開始します。", false, false),
				nil, nil,
			),
		}
		if _, err := h.slackClient.PostEphemeral(
			channelID,
			userID,
			slack.MsgOptionBlocks(blocks...),
			slack.MsgOptionTS(event.TimeStamp),
		); err != nil {
			slog.Error("Failed to post message", slog.Any("err", err))
			return
		}
	}
	var issues []jira.Issue
	// 2. Jira検索クエリの生成
	err := retry.Retry(5, 1*time.Second, func() error {
		jiraQuery, err := h.openAI.GenerateJiraQuery(messageText, lastError)
		if err != nil {
			slog.Error("Failed to generate Jira query", slog.Any("err", err))
			return err
		}

		// 3. 生成したJira検索クエリの通知
		{
			blocks := []slack.Block{
				slack.NewHeaderBlock(
					slack.NewTextBlockObject("plain_text", "🔍 Jira検索クエリ", false, false),
				),
				slack.NewDividerBlock(),
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("`%s`", jiraQuery), false, false),
					nil, nil,
				),
			}
			if _, err := h.slackClient.PostEphemeral(
				channelID,
				userID,
				slack.MsgOptionBlocks(blocks...),
				slack.MsgOptionTS(event.TimeStamp),
			); err != nil {
				slog.Error("Failed to post message", slog.Any("err", err))
				return err
			}
		}

		// 4. Jira APIで問い合わせを検索
		is, err := h.jira.FetchIssues(jiraQuery)
		if err != nil {
			slog.Error("Failed to fetch Jira issues", slog.Any("err", err))
			lastError = err
			return err
		}
		issues = is
		return nil
	})
	if err != nil {
		slog.Error("Failed to generate Jira query", slog.Any("err", err))
		h.postError(channelID, userID, "Jira問い合わせの生成に失敗しました。", event.TimeStamp)
		return
	}

	if len(issues) == 0 {
		if _, err := h.slackClient.PostEphemeral(
			channelID,
			userID,
			slack.MsgOptionText(":white_check_mark: *Jira問い合わせ結果*\n該当する問い合わせが見つかりませんでした。", false),
			slack.MsgOptionTS(event.TimeStamp),
		); err != nil {
			slog.Error("Failed to post message", slog.Any("err", err))
			return
		}
		return
	}

	// 5. Jira問い合わせ結果の通知
	{
		blocks := []slack.Block{
			slack.NewHeaderBlock(
				slack.NewTextBlockObject("plain_text", "📊 Jira問い合わせ結果", false, false),
			),
			slack.NewDividerBlock(),
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("Jira問い合わせ結果: %d件です。解析を開始します。しばらくお待ち下さい。", len(issues)), false, false),
				nil, nil,
			),
		}
		if _, err := h.slackClient.PostEphemeral(
			channelID,
			userID,
			slack.MsgOptionBlocks(blocks...),
			slack.MsgOptionTS(event.TimeStamp),
		); err != nil {
			slog.Error("Failed to post message", slog.Any("err", err))
			return
		}
	}

	svc := service.NewSelectTopIssueService(h.openAI, h.slack, h.jira)
	// 6. Jiraの問い合わせから最も類似している3件を選択
	selectedIssues, err := svc.SelectTopIssues(messageText, issues, channelID)
	if err != nil {
		slog.Error("Failed to select top issues", slog.Any("err", err))
		h.postError(channelID, userID, "Jira問い合わせの選択に失敗しました。", event.TimeStamp)
		return
	}

	if len(selectedIssues) == 0 {
		if _, err := h.slackClient.PostEphemeral(
			channelID,
			userID,
			slack.MsgOptionText(":white_check_mark: *Jira問い合わせ結果*\n類似度の高い問い合わせが見つかりませんでした。", false),
			slack.MsgOptionTS(event.TimeStamp),
		); err != nil {
			slog.Error("Failed to post message", slog.Any("err", err))
			return
		}
		return
	}

	// 7. 要約生成の実行
	if err := h.openAI.GenerateSummary(selectedIssues); err != nil {
		slog.Error("Failed to generate summary", slog.Any("err", err))
		h.postError(channelID, userID, "Jira問い合わせの要約生成に失敗しました。", event.TimeStamp)
		return
	}

	for _, issue := range selectedIssues {
		blocks := []slack.Block{
			// ヘッダー
			slack.NewHeaderBlock(
				slack.NewTextBlockObject("plain_text", "📝 Jira Issue", false, false),
			),
			slack.NewDividerBlock(),
			// Jira ID
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*🔖 Jira ID:* %s", issue.ID), false, false),
				nil, nil,
			),
			// JIRA URL
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*🔗 JIRA URL:* %s", issue.URL), false, false),
				nil, nil,
			),
			// Slack URL
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*🔗 Slack URL:* %s", issue.SlackThreadURL), false, false),
				nil, nil,
			),
			// 類似度
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*📊 類似度:* %.2f", issue.Similarity), false, false),
				nil, nil,
			),
			// サマリ見出し
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", "*📝 サマリ:*", false, false),
				nil, nil,
			),
			// サマリの本文（ボックス表示）
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(">>> %s", issue.GeneratedSummary), false, false),
				nil, nil,
			),
			slack.NewDividerBlock(),
		}
		if _, err := h.slackClient.PostEphemeral(
			channelID,
			userID,
			slack.MsgOptionBlocks(blocks...),
			slack.MsgOptionTS(event.TimeStamp),
		); err != nil {
			slog.Error("Failed to post message", slog.Any("err", err))
		}
	}
}

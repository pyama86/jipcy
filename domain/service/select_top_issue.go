package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyama86/jipcy/domain/infra"
	"github.com/pyama86/jipcy/domain/model"
	"github.com/slack-go/slack"
	"github.com/songmu/retry"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type SelectTopIssueService struct {
	openAI      *infra.OpenAI
	slack       *infra.Slack
	jira        *infra.Jira
	slackClient *slack.Client
}

// 通知メッセージの構造体
type notificationMessage struct {
	message         string
	channelID       string
	threadTimestamp string
}

func NewSelectTopIssueService(openAI *infra.OpenAI, slackInfra *infra.Slack, jira *infra.Jira, slackClient *slack.Client) *SelectTopIssueService {
	return &SelectTopIssueService{
		openAI:      openAI,
		slack:       slackInfra,
		jira:        jira,
		slackClient: slackClient,
	}
}

// 通知を順次送信するworker
func (s *SelectTopIssueService) notificationWorker(ctx context.Context, notifyCh <-chan notificationMessage, wg *sync.WaitGroup) {
	defer wg.Done()

	// rate limitを考慮した間隔で通知を送信
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Notification worker stopped by context cancellation")
			return
		case msg, ok := <-notifyCh:
			if !ok {
				slog.Info("Notification worker stopped: channel closed")
				return
			}
			// rate limitを考慮して送信
			<-ticker.C
			_, _, err := s.slackClient.PostMessage(
				msg.channelID,
				slack.MsgOptionText(msg.message, false),
				slack.MsgOptionTS(msg.threadTimestamp),
				slack.MsgOptionLinkNames(false),
			)
			if err != nil {
				// 通知失敗時はログに記録するが、処理は継続
				slog.Error("Failed to send notification (processing will continue)",
					slog.String("message", msg.message),
					slog.Any("error", err))
			}
		}
	}
}

func formatIssue(issue infra.Issue) string {
	// 新しいADF対応メソッドを使用してコメントを取得
	issueComments := issue.GetComments()
	var formattedComments []string

	for _, comment := range issueComments {
		// メンション防止のため包括的な変換を適用
		safeComment := strings.ReplaceAll(comment, "@", "＠")
		formattedComments = append(formattedComments, fmt.Sprintf("### %s", safeComment))
	}

	return fmt.Sprintf(`## 概要
%s
## 詳細
%s
## コメントの履歴
%s`, issue.Fields.Summary, issue.GetDescription(), strings.Join(formattedComments, "\n\n"))
}

// Jiraの問い合わせから最も類似している3件を選択する関数（並列化版）
func (s *SelectTopIssueService) SelectTopIssues(query string, issues []infra.Issue, channelID, threadTimestamp string) ([]model.Result, error) {
	if len(issues) == 0 {
		return []model.Result{}, nil
	}

	jiraendpoint := strings.TrimSuffix(os.Getenv("JIRA_ENDPOINT"), "/")
	workspaceURL := os.Getenv("SLACK_WORKSPACE_URL")

	// 結果を格納するためのスライス
	results := make([]model.Result, len(issues))
	var mu sync.Mutex

	// 通知用のチャンネルとworkerを起動
	ctx := context.Background()
	notifyCh := make(chan notificationMessage, 100)
	var notifyWg sync.WaitGroup
	notifyWg.Add(1)
	go s.notificationWorker(ctx, notifyCh, &notifyWg)

	// エラーグループを使用して並列処理（セマフォで並列度を制限）
	const maxConcurrency = 5
	sem := semaphore.NewWeighted(maxConcurrency)
	g, gctx := errgroup.WithContext(ctx)

	// 各issueを並列で処理
	for i, issue := range issues {
		i, issue := i, issue // ループ変数をキャプチャ
		g.Go(func() error {
			// セマフォを取得（並列度を制限）
			if err := sem.Acquire(gctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			// 処理開始のログ出力
			slog.Info("Issue processing started", slog.String("issue_key", issue.Key), slog.String("summary", issue.Fields.Summary))

			// リトライ機能付きで処理
			var result model.Result
			startTime := time.Now()

			retryErr := retry.Retry(3, 3*time.Second, func() error {
				contentSummary := formatIssue(issue)
				jiraURL := fmt.Sprintf("%s/browse/%s", jiraendpoint, issue.Key)

				// Slack検索
				threads, err := s.slack.SearchThreads(jiraURL, channelID)
				if err != nil {
					return fmt.Errorf("failed to search threads: %w", err)
				}

				slackThreadMessages, err := s.slack.FormattedSearchThreads(threads)
				if err != nil {
					return fmt.Errorf("failed to format threads: %w", err)
				}

				// OpenAI類似度計算（最もエラーが起きやすい部分）
				similarity, err := s.openAI.CalculateSimilarity(query, contentSummary, slackThreadMessages)
				if err != nil {
					return fmt.Errorf("failed to calculate similarity: %w", err)
				}

				// 類似度が0.3以下のものは除外
				if similarity < 0.3 {
					result = model.Result{} // 空の結果
					return nil
				}

				// 結果を構築
				result = model.Result{
					ID:             issue.ID,
					Summary:        issue.Fields.Summary,
					Description:    issue.GetDescription(),
					URL:            jiraURL,
					ContentSummary: contentSummary,
					Similarity:     similarity,
					SlackThread:    slackThreadMessages,
				}

				if len(threads) > 0 {
					result.SlackThreadURL = fmt.Sprintf("%s/archives/%s/p%s", workspaceURL, threads[0].ChannelID, threads[0].Timestamp)
				}

				return nil
			})

			duration := time.Since(startTime)

			if retryErr != nil {
				// エラーログ出力
				slog.Error("Issue processing failed",
					slog.String("issue_key", issue.Key),
					slog.String("summary", issue.Fields.Summary),
					slog.Duration("duration", duration),
					slog.Any("error", retryErr))

				// リトライエラーの場合はSlack通知のみ行い、エラー扱いにしない
				notifyCh <- notificationMessage{
					message:         fmt.Sprintf("❌ 処理エラー: `%s` - %s (エラー: %v)", issue.Key, issue.Fields.Summary, retryErr),
					channelID:       channelID,
					threadTimestamp: threadTimestamp,
				}
				// 空の結果を設定して処理を継続
				result = model.Result{}
			}

			// 処理完了のログ出力
			slog.Info("Issue processing completed",
				slog.String("issue_key", issue.Key),
				slog.String("summary", issue.Fields.Summary),
				slog.Float64("similarity", result.Similarity),
				slog.Duration("duration", duration))

			// Slack通知: 処理完了（類似度と共に）
			var completeMsg string
			if result.Similarity < 0.3 {
				completeMsg = fmt.Sprintf("⚪ 処理完了: `%s` - %s (類似度: %.2f - 除外)", issue.Key, issue.Fields.Summary, result.Similarity)
			} else {
				completeMsg = fmt.Sprintf("✅ 処理完了: `%s` - %s (類似度: %.2f)", issue.Key, issue.Fields.Summary, result.Similarity)
			}
			notifyCh <- notificationMessage{
				message:         completeMsg,
				channelID:       channelID,
				threadTimestamp: threadTimestamp,
			}

			// 結果を格納
			mu.Lock()
			results[i] = result
			mu.Unlock()

			return nil
		})
	}

	// 全てのgoroutineの完了を待つ
	if err := g.Wait(); err != nil {
		close(notifyCh)
		notifyWg.Wait()
		return nil, fmt.Errorf("error processing issues: %w", err)
	}

	// 通知チャンネルを閉じてworkerの終了を待つ
	close(notifyCh)
	notifyWg.Wait()

	// 結果を収集（空の結果は除外）
	var convIssues []model.Result
	for _, result := range results {
		if result.ID != "" {
			convIssues = append(convIssues, result)
		}
	}

	if len(convIssues) == 0 {
		return []model.Result{}, nil
	}

	// 類似度でソート
	sort.Slice(convIssues, func(i, j int) bool {
		return convIssues[i].Similarity > convIssues[j].Similarity
	})

	// 最も関連度が高い5件を選択
	if len(convIssues) < 5 {
		return convIssues, nil
	}
	return convIssues[:5], nil
}

package service

import (
	"context"
	"fmt"
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
)

type SelectTopIssueService struct {
	openAI      *infra.OpenAI
	slack       *infra.Slack
	jira        *infra.Jira
	slackClient *slack.Client
}

func NewSelectTopIssueService(openAI *infra.OpenAI, slackInfra *infra.Slack, jira *infra.Jira, slackClient *slack.Client) *SelectTopIssueService {
	return &SelectTopIssueService{
		openAI:      openAI,
		slack:       slackInfra,
		jira:        jira,
		slackClient: slackClient,
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

	// エラーグループを使用して並列処理
	ctx := context.Background()
	g, _ := errgroup.WithContext(ctx)

	// 各issueを並列で処理
	for i, issue := range issues {
		i, issue := i, issue // ループ変数をキャプチャ
		g.Go(func() error {

			// Slack通知: 処理開始
			if err := s.notifyProcessingStart(issue, channelID, threadTimestamp); err != nil {
				// 通知エラーはログに記録するが処理は継続
				fmt.Printf("Failed to notify processing start for issue %s: %v\n", issue.Key, err)
			}

			// リトライ機能付きで処理
			var result model.Result

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

			if retryErr != nil {
				// リトライエラーの場合はSlack通知のみ行い、エラー扱いにしない
				if err := s.notifyProcessingError(issue, retryErr, channelID, threadTimestamp); err != nil {
					fmt.Printf("Failed to notify processing error for issue %s: %v\n", issue.Key, err)
				}
				// 空の結果を設定して処理を継続
				result = model.Result{}
			}

			// Slack通知: 処理完了（類似度と共に）
			if err := s.notifyProcessingComplete(issue, result.Similarity, channelID, threadTimestamp); err != nil {
				// 通知エラーはログに記録するが処理は継続
				fmt.Printf("Failed to notify processing complete for issue %s: %v\n", issue.Key, err)
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
		return nil, fmt.Errorf("error processing issues: %w", err)
	}

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

// notifyProcessingStart は各Issueの処理開始をSlackに通知する
func (s *SelectTopIssueService) notifyProcessingStart(issue infra.Issue, channelID, threadTimestamp string) error {
	// rate limit回避のための短いsleep
	time.Sleep(200 * time.Millisecond)
	message := fmt.Sprintf("🔄 処理開始: `%s` - %s", issue.Key, issue.Fields.Summary)
	_, _, err := s.slackClient.PostMessage(
		channelID,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(threadTimestamp),
		slack.MsgOptionLinkNames(false),
	)
	return err
}

// notifyProcessingComplete は各Issueの処理完了をSlackに通知する（類似度付き）
func (s *SelectTopIssueService) notifyProcessingComplete(issue infra.Issue, similarity float64, channelID, threadTimestamp string) error {
	// rate limit回避のための短いsleep
	time.Sleep(200 * time.Millisecond)
	var message string
	if similarity < 0.3 {
		message = fmt.Sprintf("⚪ 処理完了: `%s` - %s (類似度: %.2f - 除外)", issue.Key, issue.Fields.Summary, similarity)
	} else {
		message = fmt.Sprintf("✅ 処理完了: `%s` - %s (類似度: %.2f)", issue.Key, issue.Fields.Summary, similarity)
	}
	_, _, err := s.slackClient.PostMessage(
		channelID,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(threadTimestamp),
		slack.MsgOptionLinkNames(false),
	)
	return err
}

// notifyProcessingError は各Issueの処理エラーをSlackに通知する
func (s *SelectTopIssueService) notifyProcessingError(issue infra.Issue, err error, channelID, threadTimestamp string) error {
	// rate limit回避のための短いsleep
	time.Sleep(200 * time.Millisecond)
	message := fmt.Sprintf("❌ 処理エラー: `%s` - %s (エラー: %v)", issue.Key, issue.Fields.Summary, err)
	_, _, postErr := s.slackClient.PostMessage(
		channelID,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(threadTimestamp),
		slack.MsgOptionLinkNames(false),
	)
	return postErr
}

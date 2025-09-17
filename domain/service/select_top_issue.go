package service

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyama86/jipcy/domain/infra"
	"github.com/pyama86/jipcy/domain/model"
	"github.com/songmu/retry"
)

type SelectTopIssueService struct {
	openAI *infra.OpenAI
	slack  *infra.Slack
	jira   *infra.Jira
}

func NewSelectTopIssueService(openAI *infra.OpenAI, slack *infra.Slack, jira *infra.Jira) *SelectTopIssueService {
	return &SelectTopIssueService{
		openAI: openAI,
		slack:  slack,
		jira:   jira,
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
func (s *SelectTopIssueService) SelectTopIssues(query string, issues []infra.Issue, channelID string) ([]model.Result, error) {
	if len(issues) == 0 {
		return []model.Result{}, nil
	}

	jiraendpoint := strings.TrimSuffix(os.Getenv("JIRA_ENDPOINT"), "/")
	workspaceURL := os.Getenv("SLACK_WORKSPACE_URL")

	// 結果を格納するためのチャンネル
	type processResult struct {
		result model.Result
		index  int
		err    error
	}

	resultCh := make(chan processResult, len(issues))
	var wg sync.WaitGroup

	// 各issueを並列で処理
	for i, issue := range issues {
		wg.Add(1)
		go func(idx int, iss infra.Issue) {
			defer wg.Done()

			// リトライ機能付きで処理
			var result model.Result
			var processingErr error

			retryErr := retry.Retry(3, 1*time.Second, func() error {
				contentSummary := formatIssue(iss)
				jiraURL := fmt.Sprintf("%s/browse/%s", jiraendpoint, iss.Key)

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
					ID:             iss.ID,
					Summary:        iss.Fields.Summary,
					Description:    iss.GetDescription(),
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
				processingErr = retryErr
			}

			// 結果をチャンネルに送信
			resultCh <- processResult{
				result: result,
				index:  idx,
				err:    processingErr,
			}
		}(i, issue)
	}

	// 全てのgoroutineの完了を待つ
	wg.Wait()
	close(resultCh)

	// 結果を収集
	var convIssues []model.Result
	var errors []error

	for res := range resultCh {
		if res.err != nil {
			errors = append(errors, fmt.Errorf("issue index %d: %w", res.index, res.err))
			continue
		}

		// 空の結果（類似度0.3以下）はスキップ
		if res.result.ID != "" {
			convIssues = append(convIssues, res.result)
		}
	}

	// 一部のエラーは許容するが、全てエラーの場合は失敗とする
	if len(errors) > 0 && len(convIssues) == 0 {
		return nil, fmt.Errorf("all issues failed to process: %v", errors)
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

package service

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/andygrunwald/go-jira"
	"github.com/pyama86/jipcy/domain/infra"
	"github.com/pyama86/jipcy/domain/model"
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

func formatIssue(issue jira.Issue) string {
	var comments []string
	if issue.Fields.Comments != nil {
		for _, comment := range issue.Fields.Comments.Comments {
			// メンション防止のため包括的な変換を適用
			authorName := strings.ReplaceAll(comment.Author.DisplayName, "@", "＠")
			comments = append(comments, fmt.Sprintf(`
### 作成日時:%s
- 作成者:%s
- 内容:%s`, comment.Created, authorName, comment.Body))
		}
	}
	return fmt.Sprintf(`## 概要
%s
## 詳細
%s
## コメントの履歴
%s`, issue.Fields.Summary, issue.Fields.Description, strings.Join(comments, "\n"))
}

// Jiraの問い合わせから最も類似している3件を選択する関数
func (s *SelectTopIssueService) SelectTopIssues(query string, issues []jira.Issue, channelID string) ([]model.Result, error) {
	jiraendpoint := strings.TrimSuffix(os.Getenv("JIRA_ENDPOINT"), "/")
	convIssues := []model.Result{}

	workspaceURL := os.Getenv("SLACK_WORKSPACE_URL")
	for i := range issues {
		contentSummary := formatIssue(issues[i])
		jiraURL := fmt.Sprintf("%s/browse/%s", jiraendpoint, issues[i].Key)

		threads, err := s.slack.SearchThreads(jiraURL, channelID)
		if err != nil {
			return nil, fmt.Errorf("failed to search threads: %w", err)
		}
		slackThreadMessages, err := s.slack.FormattedSearchThreads(threads)
		if err != nil {
			return nil, fmt.Errorf("failed to search threads: %w", err)
		}

		similarity, err := s.openAI.CalculateSimilarity(query, contentSummary, slackThreadMessages)
		if err != nil {
			return nil, fmt.Errorf("failed to get similarity: %w", err)
		}

		// 類似度が0.5以下のものは削除
		if similarity < 0.5 {
			continue
		}

		r := model.Result{
			ID:             issues[i].ID,
			Summary:        issues[i].Fields.Summary,
			Description:    issues[i].Fields.Description,
			URL:            jiraURL,
			ContentSummary: contentSummary,
			Similarity:     similarity,
			SlackThread:    slackThreadMessages,
		}
		if len(threads) > 0 {
			r.SlackThreadURL = fmt.Sprintf("%s/archives/%s/p%s", workspaceURL, threads[0].ChannelID, threads[0].Timestamp)
		}
		convIssues = append(convIssues, r)
	}

	// 類似度でソート
	sort.Slice(convIssues, func(i, j int) bool {
		return convIssues[i].Similarity > convIssues[j].Similarity
	})

	// 最も関連度が高い3件を選択
	if len(convIssues) < 5 {
		return convIssues, nil
	}
	return convIssues[:5], nil
}

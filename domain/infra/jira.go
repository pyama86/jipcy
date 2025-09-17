package infra

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/andygrunwald/go-jira"
)

// ADF (Atlassian Document Format) 構造体
type ADFContent struct {
	Type    string `json:"type"`
	Version int    `json:"version,omitempty"`
	Content []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	} `json:"content,omitempty"`
}

// ADFからプレーンテキストを抽出する関数
func extractTextFromADF(adf ADFContent) string {
	var texts []string
	for _, content := range adf.Content {
		for _, innerContent := range content.Content {
			if innerContent.Text != "" {
				texts = append(texts, innerContent.Text)
			}
		}
	}
	return strings.Join(texts, " ")
}

// Jira API v3と互換性のあるカスタムIssue構造体
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string     `json:"summary"`
		Description ADFContent `json:"description"`
		Comment     struct {
			Comments []struct {
				Body    ADFContent `json:"body"`
				Created string     `json:"created"`
				Author  struct {
					DisplayName string `json:"displayName"`
				} `json:"author"`
			} `json:"comments"`
			MaxResults int `json:"maxResults"`
			StartAt    int `json:"startAt"`
			Total      int `json:"total"`
		} `json:"comment"`
	} `json:"fields"`
}

// プレーンテキストとしてDescriptionを取得
func (i *Issue) GetDescription() string {
	return extractTextFromADF(i.Fields.Description)
}

// プレーンテキストとしてコメントを取得
func (i *Issue) GetComments() []string {
	var comments []string
	for _, comment := range i.Fields.Comment.Comments {
		commentText := extractTextFromADF(comment.Body)
		if commentText != "" {
			comments = append(comments, fmt.Sprintf("作成者: %s\n作成日時: %s\n内容: %s",
				comment.Author.DisplayName, comment.Created, commentText))
		}
	}
	return comments
}

type Jira struct {
	client *jira.Client
}

func NewJira() (*Jira, error) {
	tp := jira.BasicAuthTransport{
		Username: os.Getenv("JIRA_USERNAME"),
		Password: os.Getenv("JIRA_API_TOKEN"),
	}

	jiraClient, err := jira.NewClient(tp.Client(), os.Getenv("JIRA_ENDPOINT"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Jira client: %w", err)
	}
	return &Jira{
		client: jiraClient,
	}, nil
}

func (h *Jira) FetchIssues(query string) ([]Issue, error) {
	// 新しいv3 APIエンドポイントを使用
	params := url.Values{}
	params.Add("jql", query)
	params.Add("fields", "summary,description,comment")
	params.Add("maxResults", "30")

	req, err := h.client.NewRequest("GET", "rest/api/3/search/jql", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// クエリパラメーターを設定
	req.URL.RawQuery = params.Encode()

	// Jira API v3のレスポンス構造に基づいた構造体を定義
	type SearchResult struct {
		Issues []Issue `json:"issues"`
		IsLast bool    `json:"isLast"`
		Total  int     `json:"total,omitempty"`
	}

	var result SearchResult
	_, err = h.client.Do(req, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to search Jira API: %w", err)
	}

	// 空の結果は正常なケースとして扱う（エラーにしない）
	if len(result.Issues) == 0 {
		return []Issue{}, nil
	}

	return result.Issues, nil
}

func (h *Jira) FetchUser(email string) (*jira.User, error) {
	user, _, err := h.client.User.Find(email)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	return &user[0], nil
}

// project ID を取得
func (h *Jira) FetchProjectID(projectKey string) (string, error) {
	projects, _, err := h.client.Project.ListWithOptions(&jira.GetQueryOptions{
		ProjectKeys: projectKey,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get project info: %w", err)
	}
	project := (*projects)[0]
	return project.ID, nil
}

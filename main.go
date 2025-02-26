package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sort"
	"strings"

	jira "github.com/andygrunwald/go-jira"
	"github.com/joho/godotenv"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/azure"
	"github.com/openai/openai-go/option"
	"github.com/spf13/cobra"
)

func newOpenAIClient() (*openai.Client, error) {
	if os.Getenv("AZURE_OPENAI_ENDPOINT") != "" {
		return newAzureClient()
	}

	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	options := []option.RequestOption{
		option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	}

	return openai.NewClient(options...), nil
}

func newAzureClient() (*openai.Client, error) {
	key := os.Getenv("AZURE_OPENAI_KEY")
	if key == "" {
		return nil, fmt.Errorf("AZURE_OPENAI_KEY is not set")
	}
	var azureOpenAIEndpoint = os.Getenv("AZURE_OPENAI_ENDPOINT")

	var azureOpenAIAPIVersion = "2025-01-01-preview"

	if os.Getenv("AZURE_OPENAI_API_VERSION") != "" {
		azureOpenAIAPIVersion = os.Getenv("AZURE_OPENAI_API_VERSION")
	}

	return openai.NewClient(
		azure.WithEndpoint(azureOpenAIEndpoint, azureOpenAIAPIVersion),
		azure.WithAPIKey(key),
	), nil
}

// Jiraの問い合わせ内容
type JiraIssue struct {
	ID               string `json:"id"`
	Summary          string `json:"summary"`
	Description      string `json:"description"`
	URL              string `json:"url"`
	Similarity       float64
	ContentSummary   string `json:"content_summary"`
	GeneratedSummary string `json:"generated_summary"`
}

// コマンドライン引数を処理する関数
func searchJira(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatal("自然言語の入力を指定してください")
	}

	// OpenAI APIを呼び出してJira検索クエリを生成
	query := args[0]
	client, err := newOpenAIClient()
	if err != nil {
		log.Fatal("OpenAIクライアントの初期化に失敗しました:", err)
	}
	searchQuery, err := generateJiraQuery(client, query)
	if err != nil {
		log.Fatal("Jira検索クエリの生成に失敗しました:", err)
	}

	// Jira APIで検索クエリを実行
	jiraResults, err := fetchJiraIssues(searchQuery)
	if err != nil {
		log.Fatal("Jira APIの問い合わせに失敗しました:", err)
	}

	// 類似度に基づいて最も関連する3件を選択
	selectedIssues, err := selectTopIssues(client, query, jiraResults)
	if err != nil {
		log.Fatal("Jira問い合わせの選択に失敗しました:", err)
	}

	// OpenAI APIを呼び出してサマリを生成
	if err := generateSummary(client, selectedIssues); err != nil {
		log.Fatal("サマリの生成に失敗しました:", err)
	}

	fmt.Println("以下のJiraの問い合わせが見つかりました:")
	for _, issue := range selectedIssues {
		log.Printf(`


Jira ID: %s
URL: %s
類似度: %f
サマリ
%s`, issue.ID, issue.URL, issue.Similarity, issue.GeneratedSummary)
	}
}

type JiraQuery struct {
	SearchQuery string `json:"search_query"`
}

type JiraSimilarity struct {
	Similarity float64 `json:"similarity"`
}

// Jiraの検索クエリを生成する関数
func generateJiraQuery(client *openai.Client, query string) (string, error) {
	// OpenAI APIを呼び出してJira検索クエリを生成
	prompt := fmt.Sprintf(`## 依頼内容
あなたに渡す自然言語文字列に関連しそうなJiraのレコードを検索する検索クエリを生成してください。
プロジェクトキーは:%sです。
戻り値はjsonのsearch_queryにいれてください。

## 問い合わせ内容
%s`,
		os.Getenv("JIRA_PROJECT_KEY"),
		query)

	response, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		}),
		Model: openai.F(os.Getenv("OPENAI_MODEL")),
		ResponseFormat: openai.F[openai.ChatCompletionNewParamsResponseFormatUnion](
			openai.ResponseFormatJSONObjectParam{
				Type: openai.F(openai.ResponseFormatJSONObjectTypeJSONObject),
			},
		),
	})

	if err != nil {
		return "", fmt.Errorf("OpenAI API呼び出しに失敗しました: %w", err)
	}

	var searchQuery struct {
		SearchQuery string `json:"search_query"`
	}
	err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &searchQuery)
	if err != nil {
		return "", fmt.Errorf("OpenAI APIのレスポンス解析に失敗しました: %w", err)
	}
	slog.Debug("Jira検索クエリ", slog.String("search_query", searchQuery.SearchQuery))
	return searchQuery.SearchQuery, nil
}

// Jira APIで問い合わせを検索する関数
func fetchJiraIssues(query string) ([]jira.Issue, error) {

	tp := jira.BasicAuthTransport{
		Username: os.Getenv("JIRA_USERNAME"),
		Password: os.Getenv("JIRA_API_TOKEN"),
	}

	jiraClient, _ := jira.NewClient(tp.Client(), os.Getenv("JIRA_ENDPOINT"))
	issues, _, err := jiraClient.Issue.Search(query, &jira.SearchOptions{
		Fields: []string{
			"summary",
			"description",
			"comment",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("Jira APIの問い合わせに失敗しました: %w", err)
	}

	return issues, nil
}

func formatIssue(issue jira.Issue) string {
	var comments []string
	if issue.Fields.Comments != nil {
		for _, comment := range issue.Fields.Comments.Comments {
			comments = append(comments, fmt.Sprintf(`
### 作成日時:%s
- 作成者:%s
- 内容:%s`, comment.Created, comment.Author.DisplayName, comment.Body))
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
func selectTopIssues(client *openai.Client, query string, issues []jira.Issue) ([]JiraIssue, error) {

	jiraendpoint := strings.TrimSuffix(os.Getenv("JIRA_ENDPOINT"), "/")
	convIssues := []JiraIssue{}
	for i := range issues {
		contentSummary := formatIssue(issues[i])

		// 各Jira問い合わせの内容をOpenAIに送り、関連度を算出
		prompt := fmt.Sprintf(`## 依頼内容
以下のJira課題の内容は、今私が作成しようとしている課題とどれだけ類似していますか？
類似度をjsonのsimilariyというfloat型で返却してください

## 私が作成したい課題の内容
%s
## 過去に作成された課題の内容
%s`, query, contentSummary)

		response, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
			Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(prompt),
			}),
			Model: openai.F(os.Getenv("OPENAI_MODEL")),
			ResponseFormat: openai.F[openai.ChatCompletionNewParamsResponseFormatUnion](
				openai.ResponseFormatJSONObjectParam{
					Type: openai.F(openai.ResponseFormatJSONObjectTypeJSONObject),
				},
			),
		})

		if err != nil {
			return nil, err
		}

		var similarity struct {
			Similarity float64 `json:"similarity"`
		}
		err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &similarity)
		if err != nil {
			return nil, fmt.Errorf("OpenAI APIのレスポンス解析に失敗しました: %w", err)
		}

		// 類似度が0.5以下のものは削除
		if similarity.Similarity < 0.5 {
			continue
		}

		convIssues = append(convIssues, JiraIssue{
			ID:             issues[i].ID,
			Summary:        issues[i].Fields.Summary,
			Description:    issues[i].Fields.Description,
			URL:            fmt.Sprintf("%s/browse/%s", jiraendpoint, issues[i].Key),
			ContentSummary: contentSummary,
			Similarity:     similarity.Similarity,
		})
	}

	// 類似度でソート
	sort.Slice(convIssues, func(i, j int) bool {
		return convIssues[i].Similarity > convIssues[j].Similarity
	})

	// 最も関連度が高い3件を選択
	if len(convIssues) < 3 {
		return convIssues, nil
	}
	return convIssues[:3], nil
}

// OpenAI APIを呼び出してサマリを生成する関数
func generateSummary(client *openai.Client, issues []JiraIssue) error {
	for i, issue := range issues {
		prompt := fmt.Sprintf(`## 依頼内容
以下のJiraの課題の内容と、その課題の解決方法(主にコメントとして記載されている)の結果をサマリとして自然言語で返答してください。
あなたが作成した結果の用途は新しく課題をjiraに作成するかどうかを判断するためなので簡潔に類似かどうか判断できる材料をください。

## フォーマットの指定：
- 課題の概要を200文字
- 課題の解決結果を200文字

## 過去に作成された課題
%s`, issue.ContentSummary)

		response, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
			Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(prompt),
			}),
			Model: openai.F(os.Getenv("OPENAI_MODEL")),
		})

		if err != nil {
			return fmt.Errorf("OpenAI API呼び出しに失敗しました: %w", err)
		}

		issues[i].GeneratedSummary = response.Choices[0].Message.Content
	}

	return nil
}

func main() {
	// 環境変数のロード
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// コマンドラインツールのセットアップ
	var rootCmd = &cobra.Command{Use: "jipcy"}
	rootCmd.Run = searchJira
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

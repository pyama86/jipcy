package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	jira "github.com/andygrunwald/go-jira"
	"github.com/invopop/jsonschema"
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
	searchQuery := generateJiraQuery(client, query)

	// Jira APIで検索クエリを実行
	jiraResults := fetchJiraIssues(searchQuery)

	// 類似度に基づいて最も関連する3件を選択
	selectedIssues := selectTopIssues(client, query, jiraResults)

	// OpenAI APIを呼び出してサマリを生成
	if err := generateSummary(client, selectedIssues); err != nil {
		log.Fatal("サマリの生成に失敗しました:", err)
	}

	fmt.Println("以下のJiraの問い合わせが見つかりました:")
	for _, issue := range selectedIssues {
		log.Printf("Jira ID: %s, URL: %s, 類似度: %f, サマリ: %s", issue.ID, issue.URL, issue.Similarity, issue.GeneratedSummary)
	}
}

type JiraQuery struct {
	SearchQuery string `json:"search_query"`
}

type JiraSimilarity struct {
	Similarity float64 `json:"similarity"`
}

func generateSchema[T any]() interface{} {
	// Structured Outputs uses a subset of JSON schema
	// These flags are necessary to comply with the subset
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T
	schema := reflector.Reflect(v)
	return schema
}

// Jiraの検索クエリを生成する関数
func generateJiraQuery(client *openai.Client, query string) string {
	// OpenAI APIを呼び出してJira検索クエリを生成
	prompt := fmt.Sprintf("あなたに渡す自然言語文字列に関連しそうなJiraのレコードを検索する検索クエリを生成してください。プロジェクトキーは:%sです。戻り値はjsonのsearch_queryにいれてください。\n問い合わせ内容: ```\n%s\n```",
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
		log.Fatal("OpenAI API呼び出しに失敗しました:", err)
	}

	var searchQuery struct {
		SearchQuery string `json:"search_query"`
	}
	err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &searchQuery)
	if err != nil {
		log.Fatal("OpenAI APIのレスポンス解析に失敗しました:", err)
	}
	log.Printf("Jira検索クエリ: %s", searchQuery.SearchQuery)
	return searchQuery.SearchQuery
}

// Jira APIで問い合わせを検索する関数
func fetchJiraIssues(query string) []jira.Issue {

	tp := jira.BasicAuthTransport{
		Username: os.Getenv("JIRA_USERNAME"),
		Password: os.Getenv("JIRA_API_TOKEN"),
	}

	jiraClient, _ := jira.NewClient(tp.Client(), os.Getenv("JIRA_ENDPOINT"))

	issues, _, err := jiraClient.Issue.Search(query, nil)
	if err != nil {
		log.Fatal("Jira API呼び出しに失敗しました:", err)
	}

	return issues
}

// issueの内容から、descriptionとサマリとコメントを整形して返却する
func formatIssue(issue jira.Issue) string {
	// コメントは誰が書いたか、いつ書かれたか、内容を表示
	var comments []string
	if issue.Fields.Comments != nil {
		for _, comment := range issue.Fields.Comments.Comments {
			comments = append(comments, fmt.Sprintf("コメント: %s, %s, %s", comment.Author.DisplayName, comment.Created, comment.Body))
		}
	}
	return fmt.Sprintf("概要: %s\n詳細: %s\nコメント: %s", issue.Fields.Summary, issue.Fields.Description, strings.Join(comments, "\n"))
}

// Jiraの問い合わせから最も類似している3件を選択する関数
func selectTopIssues(client *openai.Client, query string, issues []jira.Issue) []JiraIssue {

	convIsssues := make([]JiraIssue, len(issues))
	for i := range issues {
		convIsssues[i] = JiraIssue{
			ID:             issues[i].ID,
			Summary:        issues[i].Fields.Summary,
			Description:    issues[i].Fields.Description,
			URL:            issues[i].Self,
			ContentSummary: formatIssue(issues[i]),
		}

		// 各Jira問い合わせの内容をOpenAIに送り、関連度を算出
		prompt := fmt.Sprintf("以下のJira問い合わせの内容は、今私が作成しようとしている問い合わせとどれだけ類似していますか？類似度をjsonのsimilariyというfloat型で返却してください\n私が問い合わせたい内容:```\n%s\n```\n過去に作成された課題の内容: ```\n%s\n```", query, convIsssues[i].ContentSummary)

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
			log.Fatal("OpenAI API呼び出しに失敗しました:", err)
		}

		var similarity struct {
			Similarity float64 `json:"similarity"`
		}
		err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &similarity)
		if err != nil {
			log.Printf("OpenAI APIのレスポンス: %s", response.Choices[0].Message.Content)
			log.Fatal("OpenAI APIのレスポンス解析に失敗しました:", err)
		}
		convIsssues[i].Similarity = similarity.Similarity
	}

	// 類似度でソート
	sort.Slice(issues, func(i, j int) bool {
		return convIsssues[i].Similarity > convIsssues[j].Similarity
	})

	// 最も関連度が高い3件を選択
	if len(convIsssues) < 3 {
		return convIsssues
	}
	return convIsssues[:3]
}

// OpenAI APIを呼び出してサマリを生成する関数
func generateSummary(client *openai.Client, issues []JiraIssue) error {
	for i, issue := range issues {
		prompt := fmt.Sprintf("以下のJiraの問い合わせ内容と、課題の解決内容(主にコメントとして記載されている)の結果をサマリとして自然言語で返答してください。あなたが作成した結果の用途は新しく課題をjiraに作成するかどうかを判断するためなので簡潔に類似かどうか判断できる材料をください。 過去に作成された課題: %s", issue.ContentSummary)

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

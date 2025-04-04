package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/azure"
	"github.com/openai/openai-go/option"
	"github.com/pyama86/jipcy/domain/model"
)

type OpenAI struct {
	client *openai.Client
}

func NewOpenAI() (*OpenAI, error) {
	client, err := newOpenAIClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OpenAI client: %w", err)
	}
	return &OpenAI{
		client: client,
	}, nil
}

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

func (h *OpenAI) GenerateSummary(issues []model.Result) error {
	for i, issue := range issues {
		prompt := fmt.Sprintf(`## 依頼内容
以下のJiraの課題の内容と、その課題の解決方法(主にコメントとして記載されている)の結果をサマリとして自然言語で返答してください。
あなたが作成した結果の用途は新しく課題をjiraに作成するかどうかを判断するためなので簡潔に類似かどうか判断できる材料をください。
またまだ未解決のものであれば嘘をつかずに、未解決と書いてください。
よくわからないことはよくわからないと書いてください。

## フォーマットの指定：
- 課題の概要を300文字
- 課題の解決結果を300文字

## 過去に作成された課題
%s

## 関連するSlackのスレッド
%s`, issue.ContentSummary, issue.SlackThread)

		response, err := h.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
			Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(prompt),
			}),
			Model: openai.F(os.Getenv("OPENAI_MODEL")),
		})

		if err != nil {
			return fmt.Errorf("failed to call OpenAI API: %w", err)
		}

		issues[i].GeneratedSummary = response.Choices[0].Message.Content
	}
	return nil
}

// Jiraの検索クエリを生成する関数
func (h *OpenAI) GenerateJiraQuery(query string, lastError error) (string, error) {
	// OpenAI APIを呼び出してJira検索クエリを生成
	prompt := fmt.Sprintf(`## 依頼内容
あなたに渡す自然言語文字列に関連しそうなJiraの課題を検索する検索クエリを生成してください。
プロジェクトキーは:%sです。
安定して検索したいので検索ワード以外のオプションは指定しないでください。
検索クエリについてはあまり具体すぎると検索結果が少なくなるので、ANDを控えて検索結果が多い程度にお願いします。
G0239422のような英数字の組み合わせはエラーコードなどである可能性が高いため、単体でキーワードにしてください。
%s
戻り値はjsonのsearch_queryにいれてください。

最後に出力されたエラー(空白の場合もあり): %s

## 問い合わせ内容
%s`,
		os.Getenv("JIRA_PROJECT_KEY"),
		os.Getenv("JIRA_SEARCH_QUERY"),
		lastError,
		query)

	response, err := h.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
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
		return "", fmt.Errorf("failed to call OpenAI API: %w", err)
	}

	var searchQuery struct {
		SearchQuery string `json:"search_query"`
	}
	err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &searchQuery)
	if err != nil {
		return "", fmt.Errorf("failed to parse OpenAI API response: %w", err)
	}
	slog.Info("Jira検索クエリ", slog.String("search_query", searchQuery.SearchQuery))
	return searchQuery.SearchQuery, nil
}

// 問い合わせとjiraの関連度を算出する関数
func (h *OpenAI) CalculateSimilarity(query, contentSummary, slackThreadMessages string) (float64, error) {
	// 各Jira問い合わせの内容をOpenAIに送り、関連度を算出
	prompt := fmt.Sprintf(`## 依頼内容
以下のJira課題の内容は、今私が作成しようとしている課題とどれだけ類似していますか？
類似度をjsonのsimilariyというfloat型で返却してください

## 私が作成したい課題の内容
%s
## 過去に作成された課題の内容
%s

## 関連するSlackのスレッド
%s`, query, contentSummary, slackThreadMessages)

	response, err := h.client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
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
		return 0, err
	}

	var similarity struct {
		Similarity float64 `json:"similarity"`
	}
	err = json.Unmarshal([]byte(response.Choices[0].Message.Content), &similarity)
	if err != nil {
		return 0, fmt.Errorf("failed to parse OpenAI API response: %w", err)
	}
	return similarity.Similarity, nil
}

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

## メンション形式について：
Slackスレッドに含まれるメンションは以下の形式で変換されています：
- 【ユーザー】＠名前 : 個人ユーザーへのメンション（例：【ユーザー】＠田中太郎）
- 【グループ】＠名前 : グループやチームへのメンション（例：【グループ】＠developers、【グループ】＠here）
- ＠ID : 変換できなかったメンション（例：＠U083Z7J2FGX）

これらの情報を参考に、課題に関わった担当者やチームを正確に識別してください。

## フォーマットの指定：
- 課題の概要を300文字
- 課題の解決結果を300文字
- この課題に関連する担当者やチーム情報（上記のメンション形式を参考に、個人とグループを区別して記載）。特定できない場合は、特定できない旨を書いてください。

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
	prompt := fmt.Sprintf(`以下の問い合わせ内容に関連するJira課題を検索するクエリを生成してください。

プロジェクトキー: %s

要件:
- 関連性の高い課題を効率的に検索できること
- エラーコード（G00000000形式）は重要なキーワードとして扱う
- 2-4個の適切なキーワードを組み合わせる
- 結果はjson形式でsearch_queryフィールドに出力

%s

前回のエラー: %s

問い合わせ内容:
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
	prompt := fmt.Sprintf(`以下の2つの課題内容の類似度を0.0-1.0で評価してください。

評価基準:
- 0.3以上: 同じ機能・システムに関する問題
- 0.5以上: 類似の症状・エラーが発生
- 0.7以上: ほぼ同じ問題

カスタマーサービスの観点で、ユーザーからの問い合わせの類似性を重視してください。

新しい課題:
%s

既存の課題:
%s

Slackスレッド:
%s

結果をjsonのsimilarityフィールド（float型）で返してください。`, query, contentSummary, slackThreadMessages)

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

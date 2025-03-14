package infra

import (
	"fmt"
	"os"
	"strings"
	"time"

	ttlcache "github.com/jellydator/ttlcache/v3"
	"github.com/pyama86/jipcy/domain/model"
	"github.com/slack-go/slack"
)

type Slack struct {
	client           *slack.Client
	channelInfoCache *ttlcache.Cache[string, *slack.Channel]
}

func NewSlack() *Slack {
	api := slack.New(os.Getenv("SLACK_USER_TOKEN"))
	return &Slack{
		client:           api,
		channelInfoCache: ttlcache.New(ttlcache.WithTTL[string, *slack.Channel](time.Hour * 24)),
	}
}

func (h *Slack) FormattedSearchThreads(threads []model.ThreadMessage) (string, error) {
	var formattedThreads []string
	for _, thread := range threads {
		formattedThreads = append(formattedThreads, fmt.Sprintf(`
### 作成日時:%s
- 作成者:%s
- 内容:%s`, thread.Timestamp, thread.User, thread.Text))
	}
	return strings.Join(formattedThreads, "\n"), nil
}

func (h *Slack) SearchThreads(keyword, channelID string) ([]model.ThreadMessage, error) {
	if os.Getenv("SLACK_CHANNEL") != "" {
		slackChannel := strings.TrimPrefix(os.Getenv("SLACK_CHANNEL"), "#")
		keyword = fmt.Sprintf("in:#%s %s", slackChannel, keyword)
	}

	searchResult, err := h.client.SearchMessages(keyword, slack.SearchParameters{
		Count:         10,
		Sort:          "timestamp",
		SortDirection: "asc",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search messages keyword:%s error: %w", keyword, err)
	}

	visitedThreads := make(map[string]bool)
	var allThreadMessages []model.ThreadMessage

	for _, match := range searchResult.Matches {
		channelID := match.Channel.ID
		history, err := h.client.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: channelID,
			Inclusive: true,
			Latest:    match.Timestamp,
			Limit:     1,
			Oldest:    match.Timestamp,
		})
		if err != nil {
			return nil, fmt.Errorf("メッセージ履歴取得に失敗しました (channel=%s, ts=%s): %w",
				channelID, match.Timestamp, err)
		}
		if len(history.Messages) == 0 {
			continue
		}

		parentMsg := history.Messages[0]
		var parentTS string
		// スレッドの場合は親メッセージのタイムスタンプを取得
		if parentMsg.ThreadTimestamp != "" {
			parentTS = parentMsg.ThreadTimestamp
		} else {
			parentTS = parentMsg.Timestamp
		}

		threadKey := channelID + ":" + parentTS
		if visitedThreads[threadKey] {
			continue
		}
		visitedThreads[threadKey] = true

		replies, _, _, err := h.client.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: parentTS,
			Inclusive: true,
			Limit:     100,
		})
		if err != nil {
			return nil, fmt.Errorf("スレッド取得に失敗しました (channel=%s, parentTS=%s): %w",
				channelID, parentTS, err)
		}

		for _, msg := range replies {
			userName := msg.User
			allThreadMessages = append(allThreadMessages, model.ThreadMessage{
				ChannelID: channelID,
				Timestamp: msg.Timestamp,
				User:      userName,
				Text:      msg.Text,
			})
		}
	}

	return allThreadMessages, nil
}

func (h *Slack) GetChannelInfo(channelID string) (*slack.Channel, error) {
	if channel := h.channelInfoCache.Get(channelID); channel != nil {
		return channel.Value(), nil
	}
	channel, err := h.client.GetConversationInfo(&slack.GetConversationInfoInput{ChannelID: channelID})
	if err != nil {
		return nil, fmt.Errorf("failed to get channel info: %w", err)
	}
	h.channelInfoCache.Set(channelID, channel, ttlcache.DefaultTTL)
	return channel, nil
}

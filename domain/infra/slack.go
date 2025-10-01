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
	client             *slack.Client
	channelInfoCache   *ttlcache.Cache[string, *slack.Channel]
	usersCache         *ttlcache.Cache[string, []slack.User]
	userNameCache      *ttlcache.Cache[string, *slack.User]
	groupsCache        *ttlcache.Cache[string, []slack.UserGroup]
	userGroupNameCache *ttlcache.Cache[string, *slack.UserGroup]
}

func NewSlack() *Slack {
	api := slack.New(os.Getenv("SLACK_USER_TOKEN"))
	s := &Slack{
		client:             api,
		channelInfoCache:   ttlcache.New(ttlcache.WithTTL[string, *slack.Channel](time.Hour * 24)),
		usersCache:         ttlcache.New(ttlcache.WithTTL[string, []slack.User](time.Hour)),
		userNameCache:      ttlcache.New(ttlcache.WithTTL[string, *slack.User](time.Hour)),
		groupsCache:        ttlcache.New(ttlcache.WithTTL[string, []slack.UserGroup](time.Hour)),
		userGroupNameCache: ttlcache.New(ttlcache.WithTTL[string, *slack.UserGroup](time.Hour)),
	}
	go s.channelInfoCache.Start()
	go s.usersCache.Start()
	go s.userNameCache.Start()
	go s.groupsCache.Start()
	go s.userGroupNameCache.Start()

	// 初期化時にユーザー情報とグループ情報をキャッシュ
	go func() {
		_, err := s.getUsers()
		if err != nil {
			fmt.Printf("Failed to initialize users cache: %v\n", err)
		}
		_, err = s.getUserGroups()
		if err != nil {
			fmt.Printf("Failed to initialize user groups cache: %v\n", err)
		}
	}()

	return s
}

func (h *Slack) FormattedSearchThreads(threads []model.ThreadMessage) (string, error) {
	var formattedThreads []string
	for _, thread := range threads {
		// ユーザーIDを表示名に変換
		userName := thread.User
		if user, err := h.GetUserByID(thread.User); err == nil {
			userName = h.GetUserPreferredName(user)
		}

		// テキスト内のメンションも変換
		convertedText := h.ConvertUserIDsToNames(thread.Text)

		formattedThreads = append(formattedThreads, fmt.Sprintf(`
### 作成日時:%s
- 作成者:%s
- 内容:%s`, thread.Timestamp, userName, convertedText))
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

func (h *Slack) getUsers() ([]slack.User, error) {
	cacheKey := "users"
	if users := h.usersCache.Get(cacheKey); users != nil {
		return users.Value(), nil
	}
	users, err := h.client.GetUsers()
	if err != nil {
		return nil, err
	}
	h.usersCache.Set(cacheKey, users, ttlcache.DefaultTTL)

	for _, u := range users {
		if u.ID != "" {
			h.userNameCache.Set(u.ID, &u, ttlcache.DefaultTTL)
		}
	}
	return users, nil
}

func (h *Slack) GetUserByID(id string) (*slack.User, error) {
	if user := h.userNameCache.Get(id); user != nil {
		return user.Value(), nil
	}

	users, err := h.getUsers()
	if err != nil {
		return nil, err
	}

	for _, u := range users {
		if u.ID == id {
			return &u, nil
		}
	}
	return nil, fmt.Errorf("user not found: %s", id)
}

func (h *Slack) GetUserPreferredName(user *slack.User) string {
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	if user.RealName != "" {
		return user.RealName
	}
	return user.Name
}

func (h *Slack) getUserGroups() ([]slack.UserGroup, error) {
	cacheKey := "user_groups"
	if groups := h.groupsCache.Get(cacheKey); groups != nil {
		return groups.Value(), nil
	}
	groups, err := h.client.GetUserGroups(
		slack.GetUserGroupsOptionIncludeUsers(true),
	)
	if err != nil {
		return nil, err
	}
	h.groupsCache.Set(cacheKey, groups, ttlcache.DefaultTTL)

	for _, g := range groups {
		if g.Handle != "" {
			h.userGroupNameCache.Set(g.Handle, &g, ttlcache.DefaultTTL)
		}
		if g.Name != "" {
			h.userGroupNameCache.Set(g.Name, &g, ttlcache.DefaultTTL)
		}
	}

	return groups, nil
}

func (h *Slack) GetUserGroupByID(id string) (*slack.UserGroup, error) {
	groups, err := h.getUserGroups()
	if err != nil {
		return nil, err
	}

	for _, g := range groups {
		if g.ID == id {
			return &g, nil
		}
	}
	return nil, fmt.Errorf("user group not found: %s", id)
}

func (h *Slack) ConvertAllMentionsToSafe(text string) string {
	result := text

	// 1. 特殊メンション変換
	result = strings.ReplaceAll(result, "<!here>", "【グループ】＠here")
	result = strings.ReplaceAll(result, "<!channel>", "【グループ】＠channel")
	result = strings.ReplaceAll(result, "<!everyone>", "【グループ】＠everyone")

	// 2. ユーザーメンション変換 <@USERID>
	result = h.convertUserMentions(result)

	// 3. グループメンション変換 <!subteam^GROUPID|name> または <!subteam^GROUPID>
	result = h.convertGroupMentions(result)

	// 4. 変換できなかった<@...>形式も安全に変換
	result = h.convertRemainingMentions(result)

	// 5. その他の@記号も全角に変換（安全のため）
	result = strings.ReplaceAll(result, "@", "＠")

	return result
}

func (h *Slack) convertUserMentions(text string) string {
	if !strings.Contains(text, "<@") {
		return text
	}

	result := text
	start := 0
	for {
		mentionStart := strings.Index(result[start:], "<@")
		if mentionStart == -1 {
			break
		}
		mentionStart += start

		mentionEnd := strings.Index(result[mentionStart:], ">")
		if mentionEnd == -1 {
			break
		}
		mentionEnd += mentionStart

		// <@USERID> の形式からUSERIDを抽出
		userID := result[mentionStart+2 : mentionEnd]

		// ユーザー情報を取得して表示名に変換
		user, err := h.GetUserByID(userID)
		if err == nil {
			displayName := h.GetUserPreferredName(user)
			replacement := "【ユーザー】＠" + displayName
			result = result[:mentionStart] + replacement + result[mentionEnd+1:]
			start = mentionStart + len(replacement)
		} else {
			start = mentionEnd + 1
		}
	}

	return result
}

func (h *Slack) convertGroupMentions(text string) string {
	if !strings.Contains(text, "<!subteam^") {
		return text
	}

	result := text
	start := 0
	for {
		mentionStart := strings.Index(result[start:], "<!subteam^")
		if mentionStart == -1 {
			break
		}
		mentionStart += start

		mentionEnd := strings.Index(result[mentionStart:], ">")
		if mentionEnd == -1 {
			break
		}
		mentionEnd += mentionStart

		// <!subteam^GROUPID|name> または <!subteam^GROUPID> の形式を解析
		mentionContent := result[mentionStart+10 : mentionEnd] // "<!subteam^" の後から

		var groupID, groupName string
		if pipeIndex := strings.Index(mentionContent, "|"); pipeIndex != -1 {
			// <!subteam^GROUPID|name> 形式
			groupID = mentionContent[:pipeIndex]
			groupName = mentionContent[pipeIndex+1:]
		} else {
			// <!subteam^GROUPID> 形式
			groupID = mentionContent
		}

		// グループ名が指定されていない場合はIDから取得を試行
		if groupName == "" {
			if group, err := h.GetUserGroupByID(groupID); err == nil {
				if group.Handle != "" {
					groupName = group.Handle
				} else if group.Name != "" {
					groupName = group.Name
				}
			}
		}

		// 変換実行
		if groupName != "" {
			replacement := "【グループ】＠" + groupName
			result = result[:mentionStart] + replacement + result[mentionEnd+1:]
			start = mentionStart + len(replacement)
		} else {
			start = mentionEnd + 1
		}
	}

	return result
}

func (h *Slack) convertRemainingMentions(text string) string {
	// 変換できなかった<@...>形式（例：U083Z7J2FGX氏のような形式）を安全に変換
	if !strings.Contains(text, "<@") {
		return text
	}

	result := text
	start := 0
	for {
		mentionStart := strings.Index(result[start:], "<@")
		if mentionStart == -1 {
			break
		}
		mentionStart += start

		mentionEnd := strings.Index(result[mentionStart:], ">")
		if mentionEnd == -1 {
			break
		}
		mentionEnd += mentionStart

		// 残っている<@...>形式を単純に削除または安全な形式に変換
		mentionContent := result[mentionStart+2 : mentionEnd]
		replacement := "＠" + mentionContent // ユーザーIDをそのまま表示（メンションは無効化）
		result = result[:mentionStart] + replacement + result[mentionEnd+1:]
		start = mentionStart + len(replacement)
	}

	return result
}

// 後方互換性のため既存の関数名も残す
func (h *Slack) ConvertUserIDsToNames(text string) string {
	return h.ConvertAllMentionsToSafe(text)
}

// PostMessage はSlackチャンネルにメッセージを投稿する
func (h *Slack) PostMessage(channelID, message string) error {
	_, _, err := h.client.PostMessage(channelID, slack.MsgOptionText(message, false))
	return err
}

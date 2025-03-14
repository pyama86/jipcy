package infra

import (
	"fmt"
	"os"

	"github.com/andygrunwald/go-jira"
)

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

func (h *Jira) FetchIssues(query string) ([]jira.Issue, error) {
	issues, _, err := h.client.Issue.Search(query, &jira.SearchOptions{
		Fields: []string{
			"summary",
			"description",
			"comment",
		},
		MaxResults: 10,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search Jira API: %w", err)
	}

	return issues, nil
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

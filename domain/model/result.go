package model

type Result struct {
	ID               string `json:"id"`
	Summary          string `json:"summary"`
	Description      string `json:"description"`
	URL              string `json:"url"`
	Similarity       float64
	ContentSummary   string `json:"content_summary"`
	GeneratedSummary string `json:"generated_summary"`
	SlackThread      string `json:"slack_thread"`
	SlackThreadURL   string `json:"slack_thread_url"`
}

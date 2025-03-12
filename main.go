package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"github.com/pyama86/jipcy/domain/infra"
	"github.com/pyama86/jipcy/handler"
)

func validate() {
	requiredEnv := []string{
		"SLACK_BOT_TOKEN",
		"SLACK_APP_TOKEN",
		"SLACK_USER_TOKEN",
		"SLACK_WORKSPACE_URL",
		"JIRA_ENDPOINT",
		"JIRA_USERNAME",
		"JIRA_API_TOKEN",
		"JIRA_PROJECT_KEY",
	}
	for _, env := range requiredEnv {
		if os.Getenv(env) == "" {
			slog.Error("required environment variable not set", slog.String("env", env))
			os.Exit(1)
		}
	}
}

func main() {
	// check exists .env
	if _, err := os.Stat(".env"); os.IsNotExist(err) {
		slog.Error("not found .env file")
		os.Exit(1)
	}

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	validate()
	slack := infra.NewSlack()

	jira, err := infra.NewJira()
	if err != nil {
		slog.Error("NewJiraAPI failed", slog.Any("err", err))
		os.Exit(1)
	}

	openAI, err := infra.NewOpenAI()
	if err != nil {
		slog.Error("NewOpenAI failed", slog.Any("err", err))
		os.Exit(1)
	}

	h := handler.NewHandler(slack, jira, openAI)

	bind := ":3000"
	if os.Getenv("LISTEN_SOCKET") != "" {
		bind = os.Getenv("LISTEN_SOCKET")
	}
	slog.Info("Server listening", slog.String("bind", bind))
	if err := h.Handle(); err != nil {
		slog.Error("Server failed", slog.Any("err", err))
		os.Exit(1)
	}
}

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sashabaranov/go-openai"
	"github.com/xanzy/go-gitlab"
)

func main() {
	gitlabToken := os.Getenv("GITLAB_API_TOKEN")
	gitlabProjectID := os.Getenv("GITLAB_PROJECT_ID")
	openaiKey := os.Getenv("OPENAI_API_KEY")

	if gitlabToken == "" || openaiKey == "" {
		fmt.Println("Error: GitLab API token and OpenAI API key are required.")
		return
	}

	git, _ := gitlab.NewClient(gitlabToken)
	openaiClient := openai.NewClient(openaiKey)

	mergeRequests, _, err := git.MergeRequests.ListProjectMergeRequests(gitlabProjectID, &gitlab.ListProjectMergeRequestsOptions{})
	if err != nil {
		fmt.Printf("Error fetching merge requests: %v\n", err)
		return
	}

	for _, mr := range mergeRequests {
		changes, _, err := git.MergeRequests.GetMergeRequestChanges(gitlabProjectID, mr.IID, nil)
		if err != nil {
			fmt.Printf("Error fetching merge request changes: %v\n", err)
			continue
		}

		code := ""
		for _, change := range changes.Changes {
			if change.NewPath != change.OldPath {
				code += fmt.Sprintf("File renamed from %s to %s\n\n", change.OldPath, change.NewPath)
			}
			code += change.Diff
		}

		prompt := fmt.Sprintf("Review the following code changes in Go:\n\n%s", code)
		completions, err := openaiClient.CreateCompletion(context.Background(),
			openai.CompletionRequest{
				Prompt: prompt,
				Model:  "text-davinci-codex-002",
			})
		if err != nil {
			fmt.Printf("Error generating code review: %v\n", err)
			continue
		}

		reviewComment := fmt.Sprintf("Code review by ChatGPT:\n%s", completions.Choices[0].Text)
		fmt.Printf("Review for merge request #%d:\n%s\n", mr.IID, reviewComment)

		// Post the review as a comment on the merge request
		comment := &gitlab.CreateMergeRequestNoteOptions{
			Body: &reviewComment,
		}
		_, _, err = git.Notes.CreateMergeRequestNote(gitlabProjectID, mr.IID, comment)
		if err != nil {
			fmt.Printf("Error posting review comment: %v\n", err)
		}
	}
}

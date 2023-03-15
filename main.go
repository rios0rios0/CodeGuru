package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sashabaranov/go-openai"
	"github.com/xanzy/go-gitlab"
)

func noIssue(message string) bool {
	result := false
	lowered := strings.ToLower(message)
	contains := []string{
		"no issue",
		"no change",
		"any issue",
	}
	for _, value := range contains {
		result = result || strings.Contains(lowered, value)
	}

	return result
}

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
		if mr.State != "opened" {
			continue
		}

		versions, _, _ := git.MergeRequests.GetMergeRequestDiffVersions(gitlabProjectID, mr.IID, nil)
		changes, _, err := git.MergeRequests.GetMergeRequestChanges(gitlabProjectID, mr.IID, nil)
		if err != nil {
			fmt.Printf("Error fetching merge request changes: %v\n", err)
			continue
		}

		for _, change := range changes.Changes {
			code := "" // this code could be outside this iteration

			if change.NewPath != change.OldPath {
				code += fmt.Sprintf("File renamed from '%s' to '%s'\n\n", change.OldPath, change.NewPath)
			} else {
				code += fmt.Sprintf("The file path is '%s' \n\n", change.NewPath)
			}
			code += change.Diff

			// This part could be outside this iteration
			prompt := fmt.Sprintf("Check if the following code changes have issues. DON'T say anything if there's no change:\n\n%s", code)
			completions, err := openaiClient.CreateCompletion(context.Background(),
				openai.CompletionRequest{
					Prompt: prompt,
					Model:  openai.GPT3TextDavinci003,
				})
			if err != nil {
				fmt.Printf("Error generating code review: %v\n", err)
				continue
			}

			reviewComment := fmt.Sprintf("Code review by ChatGPT:\n%s", completions.Choices[0].Text)
			fmt.Printf("Review for merge request #%d:\n%s\n", mr.IID, reviewComment)

			if noIssue(reviewComment) {
				continue
			}

			comment := &gitlab.CreateMergeRequestDiscussionOptions{
				Body: &reviewComment,
				Position: &gitlab.NotePosition{
					NewLine:      1,
					OldLine:      1,
					BaseSHA:      versions[0].BaseCommitSHA,
					StartSHA:     versions[0].StartCommitSHA,
					HeadSHA:      versions[0].HeadCommitSHA,
					NewPath:      change.NewPath,
					OldPath:      change.OldPath,
					PositionType: "text",
				},
			}
			_, _, err = git.Discussions.CreateMergeRequestDiscussion(gitlabProjectID, mr.IID, comment)
			if err != nil {
				fmt.Printf("Error posting review comment: %v\n", err)
			}
		}
	}
}

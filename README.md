## CodeGuru
CodeGuru is a tool that leverages the OpenAI API to automatically review code changes in GitLab merge requests and provide helpful suggestions. It streamlines the code review process and helps maintain code quality across projects.

### Features
* Fetches merge requests from a specified GitLab repository
* Reviews code changes using OpenAI's GPT model
* Posts generated code review comments on merge requests

### Prerequisites
* Go 1.16 or higher
* GitLab API token with read access to the repository
* OpenAI API key

### Dependencies
* github.com/xanzy/go-gitlab
* github.com/sashabaranov/go-openai

### Installation
1. Clone the repository:
```bash
git clone https://github.com/yourusername/codeguru.git
cd codeguru
```

2. Install dependencies:
```bash
go get github.com/xanzy/go-gitlab
go get github.com/sashabaranov/go-openai
```

3. Build the executable:
```bash
go build -o codeguru main.go
```

### Configuration
Set your GitLab API token and OpenAI API key as environment variables:
```bash
export GITLAB_API_TOKEN="your-gitlab-api-token"
export GITLAB_PROJECT_ID="your-gitlab-project-id"
export OPENAI_API_KEY="your-openai-api-key"
```

### Usage
Run the built executable:
```bash
./codeguru
```

The tool will fetch merge requests from the specified GitLab repository, review the code changes using the OpenAI API, and post the generated suggestions as comments on the merge requests.

### Notes
You may need to adjust the OpenAI API call parameters for better results.
Handle API rate limits accordingly to prevent errors and ensure smooth operation.

### License
This project is released under the MIT License. See **LICENSE** for more details.

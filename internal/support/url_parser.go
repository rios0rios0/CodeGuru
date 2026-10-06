package support

import (
	"fmt"

	gitInfra "github.com/rios0rios0/gitforge/v4/pkg/git/infrastructure"
	globalEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
)

// ParsedPRURL holds the components extracted from a pull request URL.
type ParsedPRURL struct {
	ProviderType string
	Organization string
	Project      string // Azure DevOps only
	RepoName     string
	PRID         int
}

// serviceTypeToProvider maps gitforge ServiceType to the provider name strings used by code-guru.
//
//nolint:gochecknoglobals // read-only lookup table used as a constant
var serviceTypeToProvider = map[globalEntities.ServiceType]string{
	globalEntities.UNKNOWN:     "",
	globalEntities.GITHUB:      "github",
	globalEntities.GITLAB:      "gitlab",
	globalEntities.AZUREDEVOPS: "azuredevops",
	globalEntities.BITBUCKET:   "",
	globalEntities.CODECOMMIT:  "",
	globalEntities.CODEBERG:    "",
}

// ParsePullRequestURL extracts provider, org, repo, and PR ID from a pull request URL.
// Delegates to gitforge's ParsePullRequestURL and converts the result to code-guru's ParsedPRURL.
func ParsePullRequestURL(rawURL string) (*ParsedPRURL, error) {
	parsed, err := gitInfra.ParsePullRequestURL(rawURL)
	if err != nil {
		return nil, err
	}

	providerName, ok := serviceTypeToProvider[parsed.ServiceType]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type for URL: %s", rawURL)
	}

	return &ParsedPRURL{
		ProviderType: providerName,
		Organization: parsed.Organization,
		Project:      parsed.Project,
		RepoName:     parsed.RepoName,
		PRID:         parsed.PRID,
	}, nil
}

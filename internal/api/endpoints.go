package api

import (
	"context"
	"net/url"
	"strconv"
)

type envelope[T any] struct {
	Data T `json:"data"`
}

type pagedEnvelope[T any] struct {
	Data T `json:"data"`
	Meta struct {
		CurrentPage int `json:"current_page"`
		LastPage    int `json:"last_page"`
	} `json:"meta"`
}

// Me returns the authenticated user with organization memberships.
func (c *Client) Me(ctx context.Context) (*User, error) {
	var out envelope[User]
	if err := c.get(ctx, "users/me", nil, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// Resolve trades a `<project> [environment]` slug pair for public ids. An
// unknown or invisible slug is a 404 by design (docs/CLI_DESIGN.md §3.1).
func (c *Client) Resolve(ctx context.Context, orgID, project, environment string) (*ResolvedTarget, error) {
	query := url.Values{"project": {project}}
	if environment != "" {
		query.Set("environment", environment)
	}

	var out envelope[ResolvedTarget]
	if err := c.get(ctx, "organizations/"+url.PathEscape(orgID)+"/resolve", query, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// TriggerOptions maps onto TriggerDeploymentRequest. Zero values are omitted so
// the server's own defaults (environment branch, pipeline stage selection)
// apply untouched.
type TriggerOptions struct {
	Branch                string   `json:"branch,omitempty"`
	CommitSHA             string   `json:"commit_sha,omitempty"`
	IsDryRun              bool     `json:"is_dry_run,omitempty"`
	ScheduledAt           string   `json:"scheduled_at,omitempty"`
	Comment               string   `json:"comment,omitempty"`
	IncludeOptionalStages []string `json:"include_optional_stages,omitempty"`
}

// Deploy triggers a deployment. The API answers 202 with the deployment to
// follow — it never blocks on SSH.
func (c *Client) Deploy(ctx context.Context, environmentID string, opts TriggerOptions) (*Deployment, error) {
	var out envelope[Deployment]
	if err := c.post(ctx, "environments/"+url.PathEscape(environmentID)+"/deployments", opts, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

func (c *Client) Deployment(ctx context.Context, deploymentID string) (*Deployment, error) {
	var out envelope[Deployment]
	if err := c.get(ctx, "deployments/"+url.PathEscape(deploymentID), nil, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// Logs returns chunks with seq greater than after. The server orders by seq, so
// the caller advances `after` to the highest seq it has seen.
func (c *Client) Logs(ctx context.Context, deploymentID string, after int64, perPage int) ([]LogChunk, error) {
	query := url.Values{}
	if after > 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}
	if perPage > 0 {
		query.Set("per_page", strconv.Itoa(perPage))
	}

	var out envelope[[]LogChunk]
	if err := c.get(ctx, "deployments/"+url.PathEscape(deploymentID)+"/logs", query, &out); err != nil {
		return nil, err
	}

	return out.Data, nil
}

// LatestDeployment is the most recent deployment for an environment, or nil
// when the environment has never been deployed.
func (c *Client) LatestDeployment(ctx context.Context, environmentID string) (*Deployment, error) {
	query := url.Values{"per_page": {"1"}}

	var out envelope[[]Deployment]
	if err := c.get(ctx, "environments/"+url.PathEscape(environmentID)+"/deployments", query, &out); err != nil {
		return nil, err
	}

	if len(out.Data) == 0 {
		return nil, nil
	}

	return &out.Data[0], nil
}

// Action posts one of the non-CRUD deployment sub-resources: cancel, retry,
// rollback, approve, reject.
func (c *Client) Action(ctx context.Context, deploymentID, action string, body any) (*Deployment, error) {
	var out envelope[Deployment]
	if err := c.post(ctx, "deployments/"+url.PathEscape(deploymentID)+"/"+action, body, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// Projects lists an organization's projects, following pagination to the end.
func (c *Client) Projects(ctx context.Context, orgID string) ([]Project, error) {
	var all []Project

	for page := 1; ; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}

		var out pagedEnvelope[[]Project]
		if err := c.get(ctx, "organizations/"+url.PathEscape(orgID)+"/projects", query, &out); err != nil {
			return nil, err
		}

		all = append(all, out.Data...)

		if len(out.Data) == 0 || out.Meta.LastPage <= page {
			return all, nil
		}
	}
}

// Environments lists a project's environments (unpaginated by the API).
func (c *Client) Environments(ctx context.Context, projectID string) ([]Environment, error) {
	var out envelope[[]Environment]
	if err := c.get(ctx, "projects/"+url.PathEscape(projectID)+"/environments", nil, &out); err != nil {
		return nil, err
	}

	return out.Data, nil
}

// Release is the instance's advertised CLI release metadata. Unauthenticated.
func (c *Client) Release(ctx context.Context) (*CliRelease, error) {
	var out envelope[CliRelease]
	if err := c.get(ctx, "cli/version", nil, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

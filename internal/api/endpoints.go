package api

import (
	"context"
	"encoding/json"
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
	return c.logs(ctx, "deployments/"+url.PathEscape(deploymentID)+"/logs", after, perPage)
}

func (c *Client) logs(ctx context.Context, path string, after int64, perPage int) ([]LogChunk, error) {
	query := url.Values{}
	if after > 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}
	if perPage > 0 {
		query.Set("per_page", strconv.Itoa(perPage))
	}

	var out envelope[[]LogChunk]
	if err := c.get(ctx, path, query, &out); err != nil {
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

// CommandOptions maps onto RunEnvironmentCommandRequest. The command is one
// string the server runs through the deploy user's shell; zero values are
// omitted so the server's defaults (primary server, 1800s limit) apply.
type CommandOptions struct {
	Command        string `json:"command"`
	Server         string `json:"server,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// RunCommand starts an ad-hoc command in the environment's live release. Like a
// deploy, the API answers 202 and the command runs on a worker.
func (c *Client) RunCommand(ctx context.Context, environmentID string, opts CommandOptions) (*EnvironmentCommand, error) {
	var out envelope[EnvironmentCommand]
	if err := c.post(ctx, "environments/"+url.PathEscape(environmentID)+"/commands", opts, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

func (c *Client) Command(ctx context.Context, commandID string) (*EnvironmentCommand, error) {
	var out envelope[EnvironmentCommand]
	if err := c.get(ctx, "environment-commands/"+url.PathEscape(commandID), nil, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// CommandLogs is Logs for an environment command: same seq cursor, no target.
func (c *Client) CommandLogs(ctx context.Context, commandID string, after int64, perPage int) ([]LogChunk, error) {
	return c.logs(ctx, "environment-commands/"+url.PathEscape(commandID)+"/logs", after, perPage)
}

// CancelCommand asks the worker to stop the command. It answers 202 at once;
// the status turns cancelled when the remote process is actually gone.
func (c *Client) CancelCommand(ctx context.Context, commandID string) (*EnvironmentCommand, error) {
	var out envelope[EnvironmentCommand]
	if err := c.post(ctx, "environment-commands/"+url.PathEscape(commandID)+"/cancel", nil, &out); err != nil {
		return nil, err
	}

	return &out.Data, nil
}

// Commands is the environment's most recent commands, newest first (the first
// page of a cursor-paginated history).
func (c *Client) Commands(ctx context.Context, environmentID string, perPage int) ([]EnvironmentCommand, error) {
	query := url.Values{"per_page": {strconv.Itoa(perPage)}}

	var out envelope[[]EnvironmentCommand]
	if err := c.get(ctx, "environments/"+url.PathEscape(environmentID)+"/commands", query, &out); err != nil {
		return nil, err
	}

	return out.Data, nil
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

// CreateProjectOptions omits unset fields so server defaults remain authoritative.
type CreateProjectOptions struct {
	Name           string `json:"name,omitempty"`
	Slug           string `json:"slug,omitempty"`
	Description    string `json:"description,omitempty"`
	ProjectType    string `json:"project_type,omitempty"`
	ProjectGroupID string `json:"project_group_id,omitempty"`
	PHPVersion     string `json:"php_version,omitempty"`
	BasePath       string `json:"base_path,omitempty"`
}

// CreateRepositoryOptions omits unset fields so server defaults remain authoritative.
type CreateRepositoryOptions struct {
	Provider      string `json:"provider,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CloneToken    string `json:"clone_token,omitempty"`
	CloneUsername string `json:"clone_username,omitempty"`
}

// CreateEnvironmentOptions omits unset fields so server defaults remain authoritative.
type CreateEnvironmentOptions struct {
	RepositoryID     string `json:"repository_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Slug             string `json:"slug,omitempty"`
	URL              string `json:"url,omitempty"`
	BasePath         string `json:"base_path,omitempty"`
	Branch           string `json:"branch,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
	ReleaseRetention int    `json:"release_retention,omitempty"`
	AutoDeploy       bool   `json:"auto_deploy,omitempty"`
}

// CreateProject returns the resource needed for the next repository or environment step.
func (c *Client) CreateProject(ctx context.Context, id string, opts CreateProjectOptions) (*Project, error) {
	var out envelope[Project]
	if err := c.post(ctx, "organizations/"+url.PathEscape(id)+"/projects", opts, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// CreateRepository includes any generated deploy key so the operator can install it.
func (c *Client) CreateRepository(ctx context.Context, id string, opts CreateRepositoryOptions) (*Repository, error) {
	var out envelope[Repository]
	if err := c.post(ctx, "projects/"+url.PathEscape(id)+"/repositories", opts, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// CreateEnvironment creates the environment before servers or pipelines can be attached.
func (c *Client) CreateEnvironment(ctx context.Context, id string, opts CreateEnvironmentOptions) (*Environment, error) {
	var out envelope[Environment]
	if err := c.post(ctx, "projects/"+url.PathEscape(id)+"/environments", opts, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// CopyPipeline copies the source version into a newly created environment.
func (c *Client) CopyPipeline(ctx context.Context, id string, opts CopyPipelineOptions) (*Pipeline, error) {
	var out envelope[Pipeline]
	if err := c.post(ctx, "pipelines/"+url.PathEscape(id)+"/copy", opts, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// ProjectGroups supplies the optional organization grouping choices.
func (c *Client) ProjectGroups(ctx context.Context, id string) ([]ProjectGroup, error) {
	var out envelope[[]ProjectGroup]
	if err := c.get(ctx, "organizations/"+url.PathEscape(id)+"/project-groups", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Repositories supplies the required repository choices for an environment.
func (c *Client) Repositories(ctx context.Context, id string) ([]Repository, error) {
	var out envelope[[]Repository]
	if err := c.get(ctx, "projects/"+url.PathEscape(id)+"/repositories", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Pipelines lets intake select the active version from a source environment.
func (c *Client) Pipelines(ctx context.Context, id string) ([]Pipeline, error) {
	var out envelope[[]Pipeline]
	if err := c.get(ctx, "environments/"+url.PathEscape(id)+"/pipelines", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Servers follows every page so choices include the entire organization.
func (c *Client) Servers(ctx context.Context, id string) ([]Server, error) {
	var all []Server
	for page := 1; ; page++ {
		var out pagedEnvelope[[]Server]
		if err := c.get(ctx, "organizations/"+url.PathEscape(id)+"/servers", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if len(out.Data) == 0 || out.Meta.LastPage <= page {
			return all, nil
		}
	}
}

// SyncEnvironmentServers replaces attachments in one request, preserving primary selection.
func (c *Client) SyncEnvironmentServers(ctx context.Context, id string, servers []ServerAttachment) error {
	return c.put(ctx, "environments/"+url.PathEscape(id)+"/servers", struct {
		Servers []ServerAttachment `json:"servers"`
	}{servers}, nil)
}

// CopyPipelineOptions identifies the destination without exposing pipeline internals.
type CopyPipelineOptions struct {
	EnvironmentID string `json:"environment_id,omitempty"`
}

// ExportPipeline preserves the server's deterministic YAML without re-encoding it.
func (c *Client) ExportPipeline(ctx context.Context, id string) ([]byte, []string, error) {
	response, err := c.raw(ctx, "GET", "pipelines/"+url.PathEscape(id)+"/export", nil, nil, nil, false)
	if err != nil {
		return nil, nil, err
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, nil, ParseProblem(response.Status, response.Body)
	}
	var warnings []string
	if err := json.Unmarshal([]byte(response.Headers.Get("X-Deployer-Export-Warnings")), &warnings); err != nil {
		warnings = nil
	}
	return response.Body, warnings, nil
}

// ImportPipeline leaves validation and stage schema ownership on the server.
func (c *Client) ImportPipeline(ctx context.Context, env string, document []byte) (*Pipeline, []string, error) {
	var out struct {
		Data Pipeline `json:"data"`
		Meta struct {
			Warnings []string `json:"warnings"`
		} `json:"meta"`
	}
	err := c.post(ctx, "environments/"+url.PathEscape(env)+"/pipelines/import", map[string]string{"yaml": string(document)}, &out)
	if out.Meta.Warnings == nil {
		out.Meta.Warnings = []string{}
	}
	return &out.Data, out.Meta.Warnings, err
}

// ActivatePipeline explicitly opts the imported version into deployment use.
func (c *Client) ActivatePipeline(ctx context.Context, id string) (*Pipeline, error) {
	var out envelope[Pipeline]
	err := c.post(ctx, "pipelines/"+url.PathEscape(id)+"/activate", nil, &out)
	return &out.Data, err
}

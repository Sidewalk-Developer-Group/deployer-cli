package api

import "time"

// Deployment mirrors DeploymentResource. Only the fields the CLI renders are
// declared; unknown fields are ignored so a newer API stays compatible.
type Deployment struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	TriggerSrc   string `json:"trigger_source"`
	Branch       string `json:"branch"`
	CommitSHA    string `json:"commit_sha"`
	CommitMsg    string `json:"commit_message"`
	CommitAuthor string `json:"commit_author"`
	IsDryRun     bool   `json:"is_dry_run"`
	Environment  *struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Project *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
	} `json:"environment"`
	Release *struct {
		ID         string `json:"id"`
		ReleaseRef string `json:"release_ref"`
	} `json:"release"`
	TriggeredBy *struct {
		Name string `json:"name"`
	} `json:"triggered_by"`
	CancelRequested bool       `json:"cancel_requested"`
	ErrorSummary    string     `json:"error_summary"`
	QueuedAt        *time.Time `json:"queued_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DurationMS      *int64     `json:"duration_ms"`
	CreatedAt       *time.Time `json:"created_at"`
}

// Deployment statuses (App\Modules\Deployments\Enums\DeploymentStatus).
const (
	StatusPending         = "pending"
	StatusApprovalPending = "approval_pending"
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusDegraded        = "degraded"
	StatusCancelled       = "cancelled"
	StatusRolledBack      = "rolled_back"
)

// IsTerminal mirrors DeploymentStatus::isTerminal — the follow loop stops here.
func IsTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusDegraded, StatusCancelled, StatusRolledBack:
		return true
	}

	return false
}

// Succeeded is the set of terminal statuses that mean "the release is live".
// Degraded is deliberately excluded: some targets failed, so exiting 0 would
// tell CI a partial deploy was fine.
func Succeeded(status string) bool {
	return status == StatusSucceeded
}

// LogChunk mirrors DeploymentLogResource. Seq is monotonic per deployment and
// is what the follow loop pages on.
type LogChunk struct {
	TargetID  string     `json:"target_id"`
	Seq       int64      `json:"seq"`
	Stream    string     `json:"stream"`
	Chunk     string     `json:"chunk"`
	CreatedAt *time.Time `json:"created_at"`
}

// EnvironmentCommand mirrors EnvironmentCommandResource. Its statuses are the
// queued/running/succeeded/failed/cancelled subset of the deployment ones.
// ExitCode is the remote process's exit status; nil when it is unknown (the
// connection dropped, the worker died, it never started).
type EnvironmentCommand struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Status  string `json:"status"`
	Server  *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"server"`
	TimeoutSeconds  int        `json:"timeout_seconds"`
	ExitCode        *int       `json:"exit_code"`
	CancelRequested bool       `json:"cancel_requested"`
	OutputTruncated bool       `json:"output_truncated"`
	ErrorSummary    string     `json:"error_summary"`
	CreatedAt       *time.Time `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// Project mirrors the fields of ProjectResource the CLI lists.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	ProjectType string `json:"project_type"`
	Latest      *struct {
		Status    string     `json:"status"`
		CreatedAt *time.Time `json:"created_at"`
	} `json:"latest_deployment"`
}

// Environment mirrors the fields of EnvironmentResource the CLI lists.
type Environment struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	URL             string `json:"url"`
	Branch          string `json:"branch"`
	RequiresApprove bool   `json:"requires_approval"`
	IsLocked        bool   `json:"is_locked"`
}

// ResolvedTarget mirrors ResolvedTargetResource (docs/CLI_DESIGN.md §3.1).
type ResolvedTarget struct {
	Organization struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"organization"`
	Project struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"project"`
	Environment *struct {
		ID              string `json:"id"`
		Slug            string `json:"slug"`
		Name            string `json:"name"`
		Branch          string `json:"branch"`
		RequiresApprove bool   `json:"requires_approval"`
		IsLocked        bool   `json:"is_locked"`
	} `json:"environment"`
}

// User mirrors the subset of UserResource the CLI needs: identity plus the org
// memberships that drive organization defaulting (docs/CLI_DESIGN.md §3.2).
type User struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Organizations []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Slug        string   `json:"slug"`
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
	} `json:"organizations"`
}

// CliRelease mirrors GET cli/version (docs/CLI_DESIGN.md §3.4).
type CliRelease struct {
	Latest      string `json:"latest"`
	Minimum     string `json:"minimum"`
	DownloadURL string `json:"download_url"`
	Install     struct {
		Homebrew string `json:"homebrew"`
		Script   string `json:"script"`
	} `json:"install"`
}

// Package exit owns the CLI's exit-code contract (docs/CLI_DESIGN.md §3.5).
// The codes are a public interface: CI pipelines branch on them, so they may be
// added to but never reassigned.
package exit

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
)

const (
	OK               = 0 // succeeded; for a followed deploy, the release is live
	DeploymentFailed = 1 // the deployment itself failed, or was cancelled
	Usage            = 2 // bad flags, unknown project/environment, ambiguous org
	Auth             = 3 // no token, revoked token, missing ability
	API              = 4 // transport failure, state conflict, unexpected status
	AwaitingApproval = 5 // deployment is parked on an approval gate
)

// Error carries the code the process should exit with alongside the message
// shown to the operator.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func With(code int, err error) *Error {
	return &Error{Code: code, Err: err}
}

func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// Code maps any error onto an exit code. An already-classified *Error wins;
// otherwise an API problem is classified by status.
func Code(err error) int {
	if err == nil {
		return OK
	}

	var classified *Error
	if errors.As(err, &classified) {
		return classified.Code
	}

	var problem *api.Problem
	if errors.As(err, &problem) {
		switch problem.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return Auth
		case http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadRequest:
			// 404 here is "you named something that doesn't exist (or that you
			// can't see)" — a usage error from the operator's side.
			return Usage
		default:
			return API
		}
	}

	var transport *api.TransportError
	if errors.As(err, &transport) {
		return API
	}

	return API
}

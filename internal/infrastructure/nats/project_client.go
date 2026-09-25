// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"fmt"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// projectServiceErrorCode returns the error code from a project-service error
// envelope ({"error":"not_found",...} or {"error":"internal",...}), or "" when
// no error code could be extracted (caller must classify the reply as success
// or a different failure). "" is returned for: empty body, non-JSON content,
// JSON that does not contain an "error" key, or an "error" key with an empty
// value. A non-nil error is returned only when data starts with '{' but is not
// valid JSON — the caller should surface it as an Unexpected error.
func projectServiceErrorCode(data []byte) (string, error) {
	if len(data) == 0 || data[0] != '{' {
		return "", nil
	}
	var env struct {
		Error string `json:"error"`
	}
	if jsonErr := json.Unmarshal(data, &env); jsonErr != nil {
		return "", pkgerrors.NewUnexpected("malformed project-service reply envelope", jsonErr)
	}
	return env.Error, nil
}

// ProjectClient implements port.ProjectMetadataClient over the
// `lfx.projects-api.get_name` / `lfx.projects-api.get_slug` NATS subjects
// exposed by lfx-v2-projects-service. Mirrors committee-service's
// project_retriever pattern (request payload is the project UID as raw bytes;
// reply is the value as raw bytes).
type ProjectClient struct {
	client *Client
}

// NewProjectClient wires a ProjectClient over the shared NATS client.
func NewProjectClient(client *Client) *ProjectClient {
	return &ProjectClient{client: client}
}

// Name resolves the project's display name.
func (p *ProjectClient) Name(ctx context.Context, projectUID string) (string, error) {
	return p.get(ctx, ProjectGetNameSubject, projectUID)
}

// Slug resolves the project's slug.
func (p *ProjectClient) Slug(ctx context.Context, projectUID string) (string, error) {
	return p.get(ctx, ProjectGetSlugSubject, projectUID)
}

func (p *ProjectClient) get(ctx context.Context, subject, projectUID string) (string, error) {
	if projectUID == "" {
		return "", pkgerrors.NewValidation("project_uid is required")
	}
	reply, err := p.client.Request(ctx, subject, []byte(projectUID))
	if err != nil {
		return "", err
	}
	return parseProjectReply(subject, projectUID, reply)
}

// parseProjectReply interprets a raw project-service RPC reply body.
//
// Classification:
//   - JSON error envelope ({"error":"not_found",...})   → pkgerrors.NotFound
//   - JSON error envelope ({"error":"<other code>",...}) → pkgerrors.Unexpected
//   - Any payload starting with '{'                     → pkgerrors.Unexpected
//     (see structural guarantee below)
//   - Empty body                                        → pkgerrors.Unexpected
//     (transport/dispatch failure; confirmed absences arrive as {"error":"not_found"})
//   - Plain non-empty string not starting with '{'      → success
//
// Structural guarantee: success values returned by this function never start
// with '{'. This is provable by two complementary constraints in the peer
// contract (lfx-v2-project-service PR #121):
//
//  1. Handler-layer: handleProjectGetAttribute rejects any stored value whose
//     TrimSpace'd form starts with '{' at runtime, returning RPCErrorInternal
//     rather than forwarding the ambiguous payload.
//  2. Write-layer: validateProjectName rejects new names that start with '{'
//     (after TrimSpace) in CreateProject / UpdateProjectBase, preventing new
//     ambiguous values from being stored.
//
// Combined with the '{' prefix guard in projectServiceErrorCode, these make
// success and failure payloads disjoint at the byte level — no reply starting
// with '{' is ever returned as a success string. A '{'-prefixed payload that
// does not carry a recognised error code is treated as Unexpected.
func parseProjectReply(subject, projectUID string, reply []byte) (string, error) {
	code, codeErr := projectServiceErrorCode(reply)
	if codeErr != nil {
		return "", codeErr
	}
	switch code {
	case "not_found":
		return "", pkgerrors.NewNotFound(fmt.Sprintf("project %s not found on %s", projectUID, subject))
	case "":
		// No recognised error key — either a plain success payload, an empty body,
		// or a JSON object with no 'error' key (e.g. a future response shape).
		if len(reply) == 0 {
			// An empty body is a transport/dispatch failure — project-service always
			// returns {"error":"not_found"} for missing projects.
			return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service returned empty reply for %s on %s", projectUID, subject))
		}
		if reply[0] == '{' {
			// A '{'-prefixed payload that was not classified as an error envelope is
			// ambiguous: it could be a future error shape or a project name that
			// happens to be a JSON object. We reject it as Unexpected to uphold the
			// structural guarantee that success values never start with '{'.
			return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service returned ambiguous JSON-shaped reply for %s on %s", projectUID, subject))
		}
		return string(reply), nil
	default:
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service error for %s on %s (code=%s)", projectUID, subject, code))
	}
}

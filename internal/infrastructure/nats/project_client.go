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
// envelope ({"error":"not_found",...} or {"error":"internal",...}), or "" if
// data is a success payload.
//
// Structural guarantee: only byte slices that start with '{' are tested as
// JSON objects. Project-service success payloads for all string-valued RPC
// subjects are raw UTF-8 text (display names, URL-safe slugs, HTTPS logo URLs,
// UUID strings); none of those formats begins with '{'. An error envelope is
// always a JSON object and therefore always starts with '{'. The '{' prefix
// check makes success and failure structurally disjoint at the byte level for
// every subject this client calls.
func projectServiceErrorCode(data []byte) string {
	if len(data) == 0 || data[0] != '{' {
		return ""
	}
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &env) != nil {
		return ""
	}
	return env.Error
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
// with '{'. Combined with the '{' prefix guard in projectServiceErrorCode, this
// makes success and failure payloads disjoint at the byte level — no reply
// starting with '{' is ever returned as a success string. A '{'-prefixed payload
// that does not carry a recognised error code is treated as Unexpected rather
// than silently forwarded as a project attribute value.
func parseProjectReply(subject, projectUID string, reply []byte) (string, error) {
	switch code := projectServiceErrorCode(reply); code {
	case "not_found":
		return "", pkgerrors.NewNotFound(fmt.Sprintf("project %s not found", projectUID))
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
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service error for %s (code=%s)", projectUID, code))
	}
}

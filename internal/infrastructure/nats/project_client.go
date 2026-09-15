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
//   - Empty body                                         → pkgerrors.Unexpected
//     (transport/dispatch failure; confirmed absences arrive as {"error":"not_found"})
//   - Plain non-empty string                             → success
func parseProjectReply(subject, projectUID string, reply []byte) (string, error) {
	switch code := projectServiceErrorCode(reply); code {
	case "not_found":
		return "", pkgerrors.NewNotFound(fmt.Sprintf("project %s not found", projectUID))
	case "":
		// No error key — either a plain success payload or an empty body.
		value := string(reply)
		if value == "" {
			// An empty body is a transport/dispatch failure — project-service always
			// returns {"error":"not_found"} for missing projects under the coordinated
			// RPC contract. An empty body cannot be treated as a confirmed absence.
			return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service returned empty reply for %s on %s", projectUID, subject))
		}
		return value, nil
	default:
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service error for %s (code=%s)", projectUID, code))
	}
}

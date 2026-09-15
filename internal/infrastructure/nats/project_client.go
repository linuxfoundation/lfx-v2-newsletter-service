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
// data is a normal success payload.
func projectServiceErrorCode(data []byte) string {
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
	// Project-service returns {"error":"<code>",...} on errors; any other response
	// (plain string or empty body) is a success value.
	if code := projectServiceErrorCode(reply); code != "" {
		if code == "not_found" {
			return "", pkgerrors.NewNotFound(fmt.Sprintf("project %s not found", projectUID))
		}
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("project-service error for %s (code=%s)", projectUID, code))
	}
	value := string(reply)
	if value == "" {
		return "", pkgerrors.NewNotFound(fmt.Sprintf("project attribute %s not found for uid: %s", subject, projectUID))
	}
	return value, nil
}

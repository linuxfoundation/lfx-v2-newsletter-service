// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// UserMetadataClient implements port.UserMetadataReader over the
// `lfx.auth-service.user_metadata.read` NATS subject exposed by
// lfx-v2-auth-service. Mirrors committee-service's EmailsByPrincipal pattern:
// the request body is the principal as raw bytes; the reply is a JSON envelope.
type UserMetadataClient struct {
	client *Client
}

// NewUserMetadataClient wires a UserMetadataClient over the shared NATS client.
func NewUserMetadataClient(client *Client) *UserMetadataClient {
	return &UserMetadataClient{client: client}
}

// userMetadataNATSResponse mirrors the JSON envelope documented in
// lfx-v2-auth-service/docs/user_metadata.md.
type userMetadataNATSResponse struct {
	Success bool                 `json:"success"`
	Error   string               `json:"error,omitempty"`
	Data    *userMetadataNATSDTO `json:"data,omitempty"`
}

// userMetadataNATSDTO is the subset of the auth-service user_metadata payload
// we consume. Other fields (job_title, picture, etc.) exist in the upstream
// contract but are not needed here.
type userMetadataNATSDTO struct {
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// Name resolves the user's display name. Prefers the `name` claim; falls back
// to "<given_name> <family_name>" if `name` is empty. Returns a NotFound error
// when neither is available so the caller can refuse to send rather than
// emitting an unattributed envelope.
func (u *UserMetadataClient) Name(ctx context.Context, principal string) (string, error) {
	if strings.TrimSpace(principal) == "" {
		return "", pkgerrors.NewValidation("principal is required")
	}
	reply, err := u.client.Request(ctx, AuthUserMetadataReadSubject, []byte(principal))
	if err != nil {
		return "", err
	}
	var response userMetadataNATSResponse
	if jsonErr := json.Unmarshal(reply, &response); jsonErr != nil {
		return "", pkgerrors.NewUnexpected("malformed user_metadata.read reply", jsonErr)
	}
	if !response.Success {
		msg := response.Error
		if msg == "" {
			msg = "user not found"
		}
		return "", pkgerrors.NewNotFound(fmt.Sprintf("user metadata lookup failed for principal: %s", msg))
	}
	if response.Data == nil {
		return "", pkgerrors.NewNotFound("user metadata response has no data")
	}
	if name := strings.TrimSpace(response.Data.Name); name != "" {
		return name, nil
	}
	composed := strings.TrimSpace(response.Data.GivenName + " " + response.Data.FamilyName)
	if composed != "" {
		return composed, nil
	}
	return "", pkgerrors.NewNotFound("user metadata has no display name")
}

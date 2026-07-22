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

// UserMetadataClient implements port.UserMetadataReader and
// port.UserEmailReader over auth-service's `user_metadata.read` and
// `user_emails.read` NATS subjects, respectively. Both resolve the same
// authenticated user by JWT principal, so a single client wraps the shared
// NATS connection for both lookups. Mirrors committee-service's
// EmailsByPrincipal pattern.
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

// userEmailsNATSRequest is the payload sent to lfx.auth-service.user_emails.read.
// auth-service expects JSON with `user.auth_token` set to the caller's
// principal (accepts a JWT/Authelia token, subject identifier, or LFID
// username).
type userEmailsNATSRequest struct {
	User userEmailsNATSRequestUser `json:"user"`
}

type userEmailsNATSRequestUser struct {
	AuthToken string `json:"auth_token"`
}

// userEmailsNATSResponse mirrors the JSON envelope documented in
// lfx-v2-auth-service/docs/subjects/user_emails.md.
type userEmailsNATSResponse struct {
	Success bool               `json:"success"`
	Error   string             `json:"error,omitempty"`
	Data    *userEmailsNATSDTO `json:"data,omitempty"`
}

// userEmailsNATSDTO is the subset of the auth-service user_emails payload we
// consume. AlternateEmails is not needed here — only the primary address is
// used to derive a newsletter's send-time Reply-To.
type userEmailsNATSDTO struct {
	PrimaryEmail string `json:"primary_email"`
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
		return "", pkgerrors.NewNotFound(fmt.Sprintf("user metadata lookup rejected by auth-service: %s", msg))
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

// PrimaryEmail resolves the user's primary email address. Used to derive the
// send-time Reply-To address from the same principal driving the From
// display name, so replies always reach whoever sent the newsletter rather
// than whoever last saved the draft. Callers should treat lookup failure as
// non-fatal and fall back to the newsletter's stored ed_reply_email — unlike
// Name, an unresolved sender email should not block the send.
func (u *UserMetadataClient) PrimaryEmail(ctx context.Context, principal string) (string, error) {
	if strings.TrimSpace(principal) == "" {
		return "", pkgerrors.NewValidation("principal is required")
	}
	payload, err := json.Marshal(userEmailsNATSRequest{User: userEmailsNATSRequestUser{AuthToken: principal}})
	if err != nil {
		return "", pkgerrors.NewUnexpected("marshal user_emails.read request", err)
	}
	reply, err := u.client.Request(ctx, AuthUserEmailsReadSubject, payload)
	if err != nil {
		return "", err
	}
	var response userEmailsNATSResponse
	if jsonErr := json.Unmarshal(reply, &response); jsonErr != nil {
		return "", pkgerrors.NewUnexpected("malformed user_emails.read reply", jsonErr)
	}
	if !response.Success {
		msg := response.Error
		if msg == "" {
			msg = "user not found"
		}
		return "", pkgerrors.NewNotFound(fmt.Sprintf("user emails lookup rejected by auth-service: %s", msg))
	}
	if response.Data == nil {
		return "", pkgerrors.NewNotFound("user emails response has no data")
	}
	primaryEmail := strings.TrimSpace(response.Data.PrimaryEmail)
	if primaryEmail == "" {
		return "", pkgerrors.NewNotFound("user emails response has no primary_email")
	}
	return primaryEmail, nil
}

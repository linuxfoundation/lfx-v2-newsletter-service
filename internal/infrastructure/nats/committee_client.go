// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// CommitteeClient implements port.CommitteeClient over the
// `lfx.committee-api.list_members` NATS subject exposed by
// lfx-v2-committee-service.
//
// Request payload: committee UID as raw bytes.
// Reply payload: JSON array of member records. Each record carries at minimum
// `email` and `first_name`; we ignore the rest. Empty array means a known
// committee with no members.
type CommitteeClient struct {
	client *Client
}

// NewCommitteeClient wires a CommitteeClient over the shared NATS client.
func NewCommitteeClient(client *Client) *CommitteeClient {
	return &CommitteeClient{client: client}
}

// committeeMemberDTO mirrors the relevant subset of the committee-service
// list_members reply. Fields are loose because we only consume two of them.
type committeeMemberDTO struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
}

// ListMembers fetches all members of a single committee. An empty reply (or
// a JSON empty array) returns an empty slice, not an error.
func (c *CommitteeClient) ListMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	if committeeUID == "" {
		return nil, pkgerrors.NewValidation("committee_uid is required")
	}
	reply, err := c.client.Request(ctx, CommitteeListMembersSubject, []byte(committeeUID))
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		return nil, pkgerrors.NewNotFound(fmt.Sprintf("committee not found: %s", committeeUID))
	}
	var raw []committeeMemberDTO
	if jsonErr := json.Unmarshal(reply, &raw); jsonErr != nil {
		return nil, pkgerrors.NewUnexpected("malformed committee list_members reply", jsonErr)
	}
	out := make([]model.CommitteeMember, 0, len(raw))
	for _, m := range raw {
		out = append(out, model.CommitteeMember{
			Email:     m.Email,
			FirstName: m.FirstName,
		})
	}
	return out, nil
}

// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

const audienceTestProject = "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"

func newAudienceAnalytics(t *testing.T, audience port.AudienceReader, committee port.CommitteeClient) (*AnalyticsService, *model.Newsletter) {
	t.Helper()
	groupID := "group-xyz"
	n := &model.Newsletter{
		ID: uuid.New(), ProjectUID: audienceTestProject, Status: model.StatusSent,
		GroupID: &groupID, SendProvider: model.SendProviderEmailService,
		CommitteeUIDs: []string{"committee-1"},
	}
	repo := &analyticsRepoFake{newsletter: n}
	email := &statusByGroupFake{records: []port.EmailRecipientRecord{
		{EmailID: "e1", To: "ann@x.dev", Delivered: true},
		{EmailID: "e2", To: "zed@x.dev", Delivered: true},
	}}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, audience, committee, model.SendProviderEmailService)
	return svc, n
}

func recipientNameByEmail(t *testing.T, svc *AnalyticsService, n *model.Newsletter) map[string]string {
	t.Helper()
	result, err := svc.Recipients(context.Background(), n.ProjectUID, n.ID)
	require.NoError(t, err)
	out := make(map[string]string)
	for _, r := range result.Recipients {
		out[r.Record.To] = r.Name
	}
	return out
}

func TestAnalyticsRecipients_NamesComeFromAudienceSnapshot(t *testing.T) {
	audiences := newFakeAudienceRepo()
	committee := &committeeFake{members: map[string][]model.CommitteeMember{
		"committee-1": {{Email: "ann@x.dev", FirstName: "Live", LastName: "Name"}},
	}}
	svc, n := newAudienceAnalytics(t, audiences, committee)
	audiences.put(n, n.CommitteeUIDs, model.AudienceReady, []model.CommitteeMember{
		{Email: "ann@x.dev", FirstName: "Ann", LastName: "Ames"},
		{Email: "zed@x.dev", FirstName: "Zed"},
	})

	got := recipientNameByEmail(t, svc, n)

	assert.Equal(t, "Ann Ames", got["ann@x.dev"], "the snapshot wins over a live read")
	assert.Equal(t, "Zed", got["zed@x.dev"])
	assert.Empty(t, committee.calls, "no live committee read when a snapshot is ready")
}

func TestAnalyticsRecipients_FallsBackToLiveCommitteesWithoutSnapshot(t *testing.T) {
	audiences := newFakeAudienceRepo()
	committee := &committeeFake{members: map[string][]model.CommitteeMember{
		"committee-1": {{Email: "ann@x.dev", FirstName: "Ann", LastName: "Ames"}},
	}}
	svc, n := newAudienceAnalytics(t, audiences, committee)

	got := recipientNameByEmail(t, svc, n)

	assert.Equal(t, "Ann Ames", got["ann@x.dev"])
	assert.Equal(t, []string{"committee-1"}, committee.calls, "the newsletter's own committees are read")
}

func TestAnalyticsRecipients_FallsBackWhenSnapshotIsNotReady(t *testing.T) {
	for _, status := range []model.AudienceStatus{model.AudienceProcessing, model.AudienceFailed} {
		t.Run(string(status), func(t *testing.T) {
			audiences := newFakeAudienceRepo()
			committee := &committeeFake{members: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "ann@x.dev", FirstName: "Ann"}},
			}}
			svc, n := newAudienceAnalytics(t, audiences, committee)
			audiences.put(n, n.CommitteeUIDs, status, nil)

			got := recipientNameByEmail(t, svc, n)
			assert.Equal(t, "Ann", got["ann@x.dev"])
		})
	}
}

func TestAnalyticsRecipients_FallsBackWhenAudienceReadFails(t *testing.T) {
	audiences := newFakeAudienceRepo()
	audiences.readErr = errors.New("db down")
	committee := &committeeFake{members: map[string][]model.CommitteeMember{
		"committee-1": {{Email: "ann@x.dev", FirstName: "Ann"}},
	}}
	svc, n := newAudienceAnalytics(t, audiences, committee)

	got := recipientNameByEmail(t, svc, n)
	assert.Equal(t, "Ann", got["ann@x.dev"])
}

func TestAnalyticsRecipients_NoSourcesMeansEmptyNames(t *testing.T) {
	svc, n := newAudienceAnalytics(t, nil, nil)

	got := recipientNameByEmail(t, svc, n)
	assert.Equal(t, "", got["ann@x.dev"])
	assert.Contains(t, got, "ann@x.dev", "the record is still returned")
}

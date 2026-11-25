// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// readySnapshot resolves a draft's audience with the given members.
func readySnapshot(t *testing.T, audiences *PostgresAudienceRepo, id uuid.UUID, emails ...string) {
	t.Helper()
	ctx := context.Background()
	claimed, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers(emails...)))
	require.NoError(t, audiences.MarkReady(ctx, id, len(emails), 0))
}

// setNewsletterStatus moves a newsletter to status, minting the group_id the
// sent/sending constraints require.
func setNewsletterStatus(t *testing.T, repo *PostgresNewsletterRepo, id uuid.UUID, status model.Status) {
	t.Helper()
	_, err := repo.db.NewRaw(`UPDATE newsletters SET status = ?, group_id = ?, sent_at = now() WHERE id = ?`,
		string(status), uuid.NewString(), id).Exec(context.Background())
	require.NoError(t, err)
}

func TestPurgeSentMembers_DeletesOnlySentNewslettersMembers(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)

	draft := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	readySnapshot(t, audiences, draft, "a@example.com")
	sending := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	readySnapshot(t, audiences, sending, "b@example.com")
	setNewsletterStatus(t, repo, sending, model.StatusSending)
	sent := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	readySnapshot(t, audiences, sent, "c@example.com", "d@example.com")
	setNewsletterStatus(t, repo, sent, model.StatusSent)

	purged, err := audiences.PurgeSentMembers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, purged)

	for id, want := range map[uuid.UUID]int{draft: 1, sending: 1, sent: 0} {
		members, err := audiences.ListMembers(ctx, id)
		require.NoError(t, err)
		assert.Len(t, members, want)
	}
	a, err := audiences.Get(ctx, sent)
	require.NoError(t, err)
	assert.Equal(t, 2, a.RecipientCount, "the audience row keeps its counts")
}

func TestReplaceMembers_RefusesANewsletterThatLeftDraft(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	claimed, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	require.True(t, claimed)

	setNewsletterStatus(t, repo, id, model.StatusSent)

	err = audiences.ReplaceMembers(ctx, id, testMembers("late@example.com"))
	assert.ErrorIs(t, err, domain.ErrNotFound)
	members, err := audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, members, "a job that finishes after the send writes nothing")
}

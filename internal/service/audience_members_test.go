// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

func TestBuildAudienceMembers(t *testing.T) {
	lists := []model.CommitteeMembers{
		{
			Members: []model.CommitteeMember{
				{Email: "  Alice@Example.COM ", FirstName: " First ", LastName: " Last "},
				{Email: "", FirstName: "NoEmail"},
				{Email: "not-an-address", FirstName: "NoAt"},
				{Email: "bob@example.com", FirstName: "Bob"},
			},
			SkippedNoEmail: 2,
		},
		{
			Members: []model.CommitteeMember{
				{Email: "alice@example.com", FirstName: "Duplicate"},
				{Email: "carol@example.com", FirstName: "Carol"},
				{Email: "unsub@example.com", FirstName: "Unsub"},
			},
		},
	}
	excluded := map[string]struct{}{"unsub@example.com": {}}

	got := buildAudienceMembers(lists, excluded)

	assert.Equal(t, []model.CommitteeMember{
		{Email: "alice@example.com", FirstName: "First", LastName: "Last"},
		{Email: "bob@example.com", FirstName: "Bob"},
		{Email: "carol@example.com", FirstName: "Carol"},
	}, got.Members, "emails are trimmed and lowercased, the first duplicate wins, and the order is first seen")
	assert.Equal(t, 4, got.SkippedNoEmail, "2 from the source plus the empty and the @-less address")
	assert.Equal(t, 1, got.Unsubscribed)
}

func TestBuildAudienceMembers_UnsubscribeMatchIsCaseInsensitive(t *testing.T) {
	lists := []model.CommitteeMembers{{Members: []model.CommitteeMember{{Email: "Alice@Example.com"}}}}
	got := buildAudienceMembers(lists, map[string]struct{}{"alice@example.com": {}})
	assert.Empty(t, got.Members)
	assert.Equal(t, 1, got.Unsubscribed)
}

func TestBuildAudienceMembers_CountsAnUnsubscribedDuplicateOnce(t *testing.T) {
	lists := []model.CommitteeMembers{
		{Members: []model.CommitteeMember{{Email: "a@example.com"}}},
		{Members: []model.CommitteeMember{{Email: "A@example.com"}}},
	}
	got := buildAudienceMembers(lists, map[string]struct{}{"a@example.com": {}})
	assert.Empty(t, got.Members)
	assert.Equal(t, 1, got.Unsubscribed)
}

func TestBuildAudienceMembers_Empty(t *testing.T) {
	got := buildAudienceMembers(nil, nil)
	assert.Empty(t, got.Members)
	assert.Zero(t, got.SkippedNoEmail)
}

func TestDropUnsubscribed(t *testing.T) {
	members := []model.CommitteeMember{{Email: "a@example.com"}, {Email: "b@example.com"}, {Email: "c@example.com"}}

	kept, dropped := dropUnsubscribed(members, map[string]struct{}{"b@example.com": {}})
	assert.Equal(t, []model.CommitteeMember{{Email: "a@example.com"}, {Email: "c@example.com"}}, kept)
	assert.Equal(t, 1, dropped)

	kept, dropped = dropUnsubscribed(members, nil)
	assert.Equal(t, members, kept)
	assert.Zero(t, dropped)
}

func TestSameCommitteeUIDs(t *testing.T) {
	assert.True(t, sameCommitteeUIDs([]string{"b", "a"}, []string{"a", "b"}))
	assert.True(t, sameCommitteeUIDs([]string{" a ", "a", "b"}, []string{"a", "b"}))
	assert.False(t, sameCommitteeUIDs([]string{"a"}, []string{"a", "b"}))
	assert.False(t, sameCommitteeUIDs([]string{"a", "c"}, []string{"a", "b"}))
	assert.True(t, sameCommitteeUIDs(nil, []string{}))
}

func TestCanonicalCommitteeUIDs_DoesNotChangeInput(t *testing.T) {
	in := []string{"b", "a", "b"}
	got := canonicalCommitteeUIDs(in)
	assert.Equal(t, []string{"a", "b"}, got)
	assert.Equal(t, []string{"b", "a", "b"}, in)
}

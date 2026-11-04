// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// audienceMembers is the outcome of normalising the members of every selected
// committee into one recipient list.
type audienceMembers struct {
	Members []model.CommitteeMember
	// SkippedNoEmail counts members with no usable address: those the source
	// returned without an email, and those whose email is empty or has no "@".
	SkippedNoEmail int
	// Unsubscribed counts distinct addresses dropped because they opted out.
	Unsubscribed int
}

// buildAudienceMembers merges committee member lists into one recipient list.
// It trims and lowercases each email, drops empty and "@"-less addresses,
// removes duplicates (the first occurrence wins), and drops addresses in
// excluded. skippedNoEmail is the count the committee source already skipped.
// Members come back in first-seen order.
func buildAudienceMembers(lists []model.CommitteeMembers, excluded map[string]struct{}) audienceMembers {
	var out audienceMembers
	seen := make(map[string]struct{})
	for _, list := range lists {
		out.SkippedNoEmail += list.SkippedNoEmail
		for _, m := range list.Members {
			email := strings.ToLower(strings.TrimSpace(m.Email))
			if email == "" || !strings.Contains(email, "@") {
				out.SkippedNoEmail++
				continue
			}
			if _, ok := seen[email]; ok {
				continue
			}
			seen[email] = struct{}{}
			if _, unsub := excluded[email]; unsub {
				out.Unsubscribed++
				continue
			}
			out.Members = append(out.Members, model.CommitteeMember{
				Email:     email,
				FirstName: strings.TrimSpace(m.FirstName),
				LastName:  strings.TrimSpace(m.LastName),
			})
		}
	}
	return out
}

// dropUnsubscribed returns members minus any address in excluded, and how many
// it dropped. Emails in members are already normalised.
func dropUnsubscribed(members []model.CommitteeMember, excluded map[string]struct{}) ([]model.CommitteeMember, int) {
	if len(excluded) == 0 {
		return members, 0
	}
	out := make([]model.CommitteeMember, 0, len(members))
	dropped := 0
	for _, m := range members {
		if _, unsub := excluded[strings.ToLower(strings.TrimSpace(m.Email))]; unsub {
			dropped++
			continue
		}
		out = append(out, m)
	}
	return out, dropped
}

// canonicalCommitteeUIDs trims, deduplicates and sorts a committee UID list so
// two lists can be compared as sets.
func canonicalCommitteeUIDs(in []string) []string {
	out := normalizeCommitteeUIDs(in)
	if out == nil {
		return []string{}
	}
	sort.Strings(out)
	return out
}

// sameCommitteeUIDs reports whether two committee UID lists hold the same set.
func sameCommitteeUIDs(a, b []string) bool {
	ca, cb := canonicalCommitteeUIDs(a), canonicalCommitteeUIDs(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// errEmptyCommittee, errCommitteeOtherProject and errCommitteeUnknownProject
// are the ways a committee read is refused. All wrap domain.ErrUnprocessable.
var (
	errEmptyCommittee          = fmt.Errorf("%w: committee returned no members", domain.ErrUnprocessable)
	errCommitteeOtherProject   = fmt.Errorf("%w: committee belongs to another project", domain.ErrUnprocessable)
	errCommitteeUnknownProject = fmt.Errorf("%w: committee members have no project", domain.ErrUnprocessable)
)

// committeeReadRules sets how strict readCommittees is.
type committeeReadRules struct {
	// requireMembers fails the read when a committee returns no members at all
	// (none with an email and none skipped). The query service answers an
	// unknown committee, or one this service is not allowed to see, with an empty
	// page, so an empty result is far more likely a missing access grant or a
	// mistyped UID than a real empty committee.
	requireMembers bool
}

// readCommittees reads every committee in parallel. The errgroup cancels the
// other reads as soon as one fails.
//
// This service reads as its own M2M identity, which can see every project, so
// this is where a committee is checked against projectUID. A committee with
// members must have at least one member indexed under projectUID and none under
// another project. Members whose source gives no project are accepted only
// alongside a member that proves the project, because a member's project is
// copied from its committee.
func readCommittees(ctx context.Context, client port.CommitteeClient, projectUID string, committeeUIDs []string, rules committeeReadRules) ([]model.CommitteeMembers, error) {
	lists := make([]model.CommitteeMembers, len(committeeUIDs))
	g, gctx := errgroup.WithContext(ctx)
	for i, uid := range committeeUIDs {
		g.Go(func() error {
			list, err := client.ListMembers(gctx, uid)
			if err != nil {
				return err
			}
			if rules.requireMembers && len(list.Members) == 0 && list.SkippedNoEmail == 0 {
				return fmt.Errorf("committee %s: %w", uid, errEmptyCommittee)
			}
			if err := checkCommitteeProject(list.Members, projectUID); err != nil {
				slog.WarnContext(gctx, "committee read: committee refused for this project",
					"committee_uid", uid,
					"project_uid", projectUID,
					"error", err,
				)
				return fmt.Errorf("committee %s: %w", uid, err)
			}
			lists[i] = list
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return lists, nil
}

// checkCommitteeProject reports whether members belong to a committee of
// projectUID. No members is not an error: there is nothing to return.
func checkCommitteeProject(members []model.CommitteeMember, projectUID string) error {
	proven := false
	for _, m := range members {
		switch m.ProjectUID {
		case "":
		case projectUID:
			proven = true
		default:
			return errCommitteeOtherProject
		}
	}
	if len(members) > 0 && !proven {
		return errCommitteeUnknownProject
	}
	return nil
}

// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// applyPublicationSender returns the From display name and Reply-To for draft,
// taking the publication's when it fixes them. Implicit publications leave both
// unset, so their editions keep the sender-resolved values unchanged.
//
// A reply-to outside the allowed domains is refused when the publication is
// saved. The check here covers the allowlist changing afterwards: the override
// is skipped with a warning and the existing Reply-To chain applies. A failed
// lookup fails the request before anything is claimed
// or sent, so a mailing can't go out under the wrong sender.
func (o *SendOrchestrator) applyPublicationSender(ctx context.Context, draft *model.Newsletter, fromName, replyTo string) (string, string, error) {
	if o.publications == nil || draft.PublicationUID == nil {
		return fromName, replyTo, nil
	}
	pub, err := o.publications.GetPublication(ctx, draft.ProjectUID, *draft.PublicationUID)
	if err != nil {
		return "", "", fmt.Errorf("load publication: %w", err)
	}
	if pub.FromName != nil {
		fromName = *pub.FromName
	}
	if pub.ReplyToEmail != nil {
		if domainAllowed(*pub.ReplyToEmail, o.replyToAllowedDomains) {
			replyTo = *pub.ReplyToEmail
		} else {
			slog.WarnContext(ctx, "reply-to: publication reply_to_email domain not in allowed reply-to domains, ignoring the override",
				"publication_id", pub.ID,
				"domain", domainOf(*pub.ReplyToEmail),
			)
			// The edition may carry a copy of a publication address from when it
			// was created, as its stored reply-to fallback. Drop any fallback the
			// allowlist no longer accepts, or email-service refuses every send.
			if !domainAllowed(strings.TrimSpace(replyTo), o.replyToAllowedDomains) {
				replyTo = ""
			}
		}
	}
	return fromName, replyTo, nil
}

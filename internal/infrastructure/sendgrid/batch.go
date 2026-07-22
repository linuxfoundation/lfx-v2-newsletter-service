// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// maxPersonalizationsPerCall is SendGrid's cap on personalizations in one
// mail/send call. A batch larger than this is split across calls that share the
// same batch_id, so a pause/cancel applies to the whole campaign. (This is the
// mail/send POST-body limit; the ~100-id cap gatewaze hit was on the Email
// Activity API query URL used by the reconcile worker, not here.)
const maxPersonalizationsPerCall = 1000

// BatchRecipient is one recipient in a batched send.
type BatchRecipient struct {
	To string
	// SendAt is the recipient's scheduled dispatch instant (SendGrid requires
	// <=72h out). Zero sends immediately.
	SendAt time.Time
	// Substitutions fill merge tokens in the shared subject/body for this
	// recipient (e.g. an unsubscribe-URL sentinel -> the per-recipient link).
	Substitutions map[string]string
}

// BatchInput is a batched (personalized) send: one subject/body delivered to
// many recipients, each with its own send_at and substitutions, under a shared
// group_id (analytics) and optional batch_id (SendGrid pause/cancel).
type BatchInput struct {
	Subject         string
	HTML            string
	Text            string
	From            string
	FromDisplayName string
	ReplyTo         string
	GroupID         string
	BatchID         string
	Recipients      []BatchRecipient
}

// BatchResult is the per-recipient outcome of a batched send. EmailID is the
// minted tracking handle (the webhook back-map key); Err is set to the chunk's
// send error for every recipient in a chunk whose mail/send call failed.
type BatchResult struct {
	To      string
	EmailID string
	Err     error
}

// SendBatch delivers one subject/body to many recipients using SendGrid
// personalizations, chunked at maxPersonalizationsPerCall. Each personalization
// carries the recipient's send_at, per-recipient custom_args (a minted email_id
// + the shared group_id, so the event webhook can map events back), and
// substitutions. A successful chunk records each recipient's send in the
// engagement store.
//
// A partial failure is not fatal: the returned slice has a per-recipient outcome
// (Err set for a failed chunk), so a caller can retry only the failed recipients.
// Only an up-front validation problem (empty body) returns a non-nil error.
func (d *Dispatcher) SendBatch(ctx context.Context, in BatchInput) ([]BatchResult, error) {
	if strings.TrimSpace(in.HTML) == "" && strings.TrimSpace(in.Text) == "" {
		return nil, pkgerrors.NewValidation("sendgrid: at least one of HTML or Text body is required")
	}
	if len(in.Recipients) == 0 {
		return nil, nil
	}

	groupID := in.GroupID
	if strings.TrimSpace(groupID) == "" {
		groupID = uuid.NewString()
	}
	from := address{Email: in.From, Name: in.FromDisplayName}
	if strings.TrimSpace(from.Email) == "" {
		from = address{Email: d.defaultFrom, Name: d.defaultFromName}
	}
	if !d.fromDomainAllowed(from.Email) {
		return nil, pkgerrors.NewValidation(fmt.Sprintf("sendgrid: From domain %q is not an authenticated sending domain", domainOf(from.Email)))
	}

	var contents []content
	if strings.TrimSpace(in.Text) != "" {
		contents = append(contents, content{Type: "text/plain", Value: in.Text})
	}
	if strings.TrimSpace(in.HTML) != "" {
		contents = append(contents, content{Type: "text/html", Value: in.HTML})
	}

	results := make([]BatchResult, 0, len(in.Recipients))
	for start := 0; start < len(in.Recipients); start += maxPersonalizationsPerCall {
		end := min(start+maxPersonalizationsPerCall, len(in.Recipients))
		chunk := in.Recipients[start:end]

		persons := make([]personalization, 0, len(chunk))
		emailIDs := make([]string, len(chunk))
		for i, r := range chunk {
			eid := uuid.NewString()
			emailIDs[i] = eid
			p := personalization{
				To:         []address{{Email: r.To}},
				CustomArgs: map[string]string{customArgEmailID: eid, customArgGroupID: groupID},
			}
			if !r.SendAt.IsZero() {
				p.SendAt = r.SendAt.Unix()
			}
			if len(r.Substitutions) > 0 {
				p.Substitutions = r.Substitutions
			}
			persons = append(persons, p)
		}

		reqBody := mailSendRequest{
			Personalizations: persons,
			From:             from,
			Subject:          in.Subject,
			Content:          contents,
			BatchID:          in.BatchID,
		}
		if strings.TrimSpace(in.ReplyTo) != "" {
			reqBody.ReplyTo = &address{Email: in.ReplyTo}
		}
		if d.sandboxMode {
			reqBody.MailSettings = &mailSettings{SandboxMode: &toggle{Enable: true}}
		}

		err := d.postMailSend(ctx, reqBody)
		for i, r := range chunk {
			results = append(results, BatchResult{To: r.To, EmailID: emailIDs[i], Err: err})
			if err == nil {
				d.recordSent(ctx, emailIDs[i], groupID, r.To)
			}
		}
	}
	return results, nil
}

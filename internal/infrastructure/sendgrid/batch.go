// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// maxPersonalizationsPerCall is SendGrid's cap on personalizations in one
// mail/send call. A batch larger than this is split across calls that share the
// same batch_id, so a pause/cancel applies to the whole campaign. (This is the
// mail/send POST-body limit; the ~100-id cap gatewaze hit was on the Email
// Activity API query URL used by the reconcile worker, not here.)
const maxPersonalizationsPerCall = 1000

// maxSendAtWindow is SendGrid's cap on how far ahead a personalization's
// send_at may be scheduled. A send_at beyond this is rejected per-recipient
// before dispatch.
const maxSendAtWindow = 72 * time.Hour

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
// A batch-level validation problem returns a non-nil top-level error and no
// results: an empty body (no HTML or Text) or a From whose domain is not
// authenticated. Per-recipient validation (empty To, out-of-window send_at) is
// reported on that recipient's result instead, not as a top-level error.
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

	// Validate send_at up front. SendGrid rejects an entire mail/send call with
	// 400 if any personalization's send_at is beyond its 72h window, which would
	// fail every recipient in the chunk. Reject an out-of-window recipient
	// individually instead, so one bad timestamp can't sink a 1000-recipient
	// chunk. A past/zero send_at is fine (SendGrid sends immediately).
	// Validate each recipient up front, keeping its input position so the returned
	// results are index-aligned with in.Recipients (a caller can pair every
	// outcome to its input even when some are rejected, addresses are normalized,
	// or the list has duplicates).
	now := time.Now()
	type validRecipient struct {
		r   BatchRecipient
		idx int
	}
	results := make([]BatchResult, len(in.Recipients))
	valid := make([]validRecipient, 0, len(in.Recipients))
	for i, r := range in.Recipients {
		// A syntactically invalid To (empty, blank, or e.g. "a@") makes SendGrid
		// reject the whole mail/send chunk, so validate each address here and fail
		// only the bad recipient rather than sink up to 999 valid ones with it.
		// Use the parsed mailbox (not the raw string): mail.ParseAddress also
		// accepts "Name <addr>" forms, and SendGrid's address.email field wants
		// only the addr-spec, so the display-name form would otherwise be rejected.
		parsed, err := mail.ParseAddress(strings.TrimSpace(r.To))
		if err != nil {
			results[i] = BatchResult{To: r.To, Err: pkgerrors.NewValidation(fmt.Sprintf("sendgrid: invalid recipient address %q", r.To))}
			continue
		}
		r.To = parsed.Address
		if !r.SendAt.IsZero() {
			switch {
			case r.SendAt.After(now.Add(maxSendAtWindow)):
				results[i] = BatchResult{To: r.To, Err: pkgerrors.NewValidation(fmt.Sprintf("sendgrid: send_at %s is more than 72h out", r.SendAt.UTC().Format(time.RFC3339)))}
				continue
			case r.SendAt.Before(now):
				// SendGrid rejects a chunk whose send_at is in the past; a schedule
				// that elapsed before dispatch means "send now", so drop it to zero.
				r.SendAt = time.Time{}
			}
		}
		valid = append(valid, validRecipient{r: r, idx: i})
	}

	for start := 0; start < len(valid); start += maxPersonalizationsPerCall {
		end := min(start+maxPersonalizationsPerCall, len(valid))
		chunk := valid[start:end]

		persons := make([]personalization, 0, len(chunk))
		emailIDs := make([]string, len(chunk))
		for i, vr := range chunk {
			eid := uuid.NewString()
			emailIDs[i] = eid
			p := personalization{
				To:         []address{{Email: vr.r.To}},
				CustomArgs: map[string]string{customArgEmailID: eid, customArgGroupID: groupID},
			}
			if !vr.r.SendAt.IsZero() {
				p.SendAt = vr.r.SendAt.Unix()
			}
			if len(vr.r.Substitutions) > 0 {
				p.Substitutions = vr.r.Substitutions
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
		reqBody.ReplyTo = d.allowedReplyTo(ctx, in.ReplyTo)
		if d.sandboxMode {
			reqBody.MailSettings = &mailSettings{SandboxMode: &toggle{Enable: true}}
		}

		err := d.postMailSend(ctx, reqBody)
		var sent []port.SentRow
		if err == nil {
			sent = make([]port.SentRow, 0, len(chunk))
		}
		sentAt := time.Now().UTC()
		for i, vr := range chunk {
			results[vr.idx] = BatchResult{To: vr.r.To, EmailID: emailIDs[i], Err: err}
			if err == nil {
				sent = append(sent, port.SentRow{EmailID: emailIDs[i], GroupID: groupID, To: vr.r.To, SentAt: sentAt})
			}
		}
		// One bulk insert per accepted chunk instead of a round trip per recipient.
		d.recordSentBatch(ctx, sent)
	}
	return results, nil
}

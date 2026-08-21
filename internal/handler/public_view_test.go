// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// fakePublicViewRepo is a minimal port.NewsletterRepository stub. Only Get is
// exercised by PublicView; every other method panics if called so a future
// change accidentally routing through them fails loudly in tests.
type fakePublicViewRepo struct {
	newsletters map[uuid.UUID]*model.Newsletter
	getErr      error
}

func (f *fakePublicViewRepo) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	n, ok := f.newsletters[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return n, nil
}

func (f *fakePublicViewRepo) Create(context.Context, *model.Newsletter) error {
	panic("not implemented")
}
func (f *fakePublicViewRepo) List(context.Context, string) ([]*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) ListAll(context.Context, port.ListFilters) (*port.ListPage, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) ListSentByCommittee(context.Context, string, string) (*port.ListPage, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) Update(context.Context, *model.Newsletter, int64) (*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) Delete(context.Context, uuid.UUID) error { panic("not implemented") }
func (f *fakePublicViewRepo) MarkSending(context.Context, uuid.UUID, string, string, int, int64, *time.Time, string) (*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) MarkSent(context.Context, uuid.UUID, time.Time, int64) (*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) MarkScheduled(context.Context, uuid.UUID, int64) (*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) RevertScheduled(context.Context, uuid.UUID, int64, *string) (*model.Newsletter, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) SettleDueScheduled(context.Context, time.Time) (int64, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) RevertSending(context.Context, uuid.UUID) error {
	panic("not implemented")
}
func (f *fakePublicViewRepo) RecoverStuckSending(context.Context, time.Duration) (int64, error) {
	panic("not implemented")
}
func (f *fakePublicViewRepo) RecordOpen(context.Context, uuid.UUID, string) error {
	panic("not implemented")
}
func (f *fakePublicViewRepo) Analytics(context.Context, uuid.UUID) (*model.Analytics, error) {
	panic("not implemented")
}

func newPublicViewHandler(newsletters map[uuid.UUID]*model.Newsletter) *Handler {
	repo := &fakePublicViewRepo{newsletters: newsletters}
	return &Handler{
		newsletter: service.NewNewsletterService(repo),
		project:    stubProjectClient{},
	}
}

func newPublicViewRequest(projectUID, newsletterUID string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodGet, "/projects/"+projectUID+"/newsletters/"+newsletterUID+"/public", nil)
	req.SetPathValue("project_uid", projectUID)
	req.SetPathValue("newsletter_uid", newsletterUID)
	return httptest.NewRecorder(), req
}

func TestPublicViewSuccess(t *testing.T) {
	id := uuid.New()
	sentAt := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{
		id: {
			ID:         id,
			ProjectUID: "proj-1",
			Subject:    "January update",
			BodyHTML:   "<p>Hello</p>",
			Status:     model.StatusSent,
			SentAt:     &sentAt,
		},
	})

	w, req := newPublicViewRequest("proj-1", id.String())
	h.PublicView(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want public, max-age=300", cc)
	}
	body := w.Body.String()
	for _, want := range []string{"January update", "Hello", "CNCF"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

func TestPublicViewNotFound(t *testing.T) {
	h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{})

	w, req := newPublicViewRequest("proj-1", uuid.New().String())
	h.PublicView(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestPublicViewWrongProject(t *testing.T) {
	id := uuid.New()
	sentAt := time.Now()
	h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{
		id: {
			ID:         id,
			ProjectUID: "proj-1",
			Subject:    "January update",
			BodyHTML:   "<p>Hello</p>",
			Status:     model.StatusSent,
			SentAt:     &sentAt,
		},
	})

	// Same newsletter id, different project in the URL — must not leak.
	w, req := newPublicViewRequest("proj-2", id.String())
	h.PublicView(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

// TestPublicViewUndispatched covers the states whose body has not reached any
// recipient. Each must be indistinguishable from a newsletter that does not
// exist, so the permalink cannot be used to probe for unpublished content.
func TestPublicViewUndispatched(t *testing.T) {
	future := time.Now().UTC().Add(2 * time.Hour)
	cases := []struct {
		name        string
		status      model.Status
		scheduledAt *time.Time
	}{
		{name: "draft", status: model.StatusDraft},
		{name: "draft with saved schedule", status: model.StatusDraft, scheduledAt: &future},
		// The fan-out is arming a future release, so nothing is delivered yet.
		{name: "sending an armed future schedule", status: model.StatusSending, scheduledAt: &future},
		// The provider is holding the batch until ScheduledAt.
		{name: "scheduled for the future", status: model.StatusScheduled, scheduledAt: &future},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{
				id: {
					ID:          id,
					ProjectUID:  "proj-1",
					Subject:     "Not delivered yet",
					BodyHTML:    "<p>Hello</p>",
					Status:      tc.status,
					ScheduledAt: tc.scheduledAt,
				},
			})

			w, req := newPublicViewRequest("proj-1", id.String())
			h.PublicView(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 for status=%s, body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

// TestPublicViewDispatchedBeforeSettled is the regression test for the fan-out
// race. The orchestrator embeds the permalink in the email chrome before it
// fans out, and marks the row sent only after the fan-out finishes. A recipient
// at the front of a large fan-out opens the link while the row is still
// 'sending', and must get the newsletter rather than a 404.
func TestPublicViewDispatchedBeforeSettled(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Hour)
	cases := []struct {
		name        string
		status      model.Status
		scheduledAt *time.Time
	}{
		{name: "immediate send still fanning out", status: model.StatusSending},
		// A plain immediate send never clears a draft's saved-but-unarmed
		// scheduled_at, so a past value can survive into 'sending'.
		{name: "sending with a past schedule", status: model.StatusSending, scheduledAt: &past},
		// The provider released the batch; the recovery sweep has not yet
		// settled the row to 'sent'.
		{name: "scheduled release already passed", status: model.StatusScheduled, scheduledAt: &past},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{
				id: {
					ID:          id,
					ProjectUID:  "proj-1",
					Subject:     "January update",
					BodyHTML:    "<p>Hello</p>",
					Status:      tc.status,
					ScheduledAt: tc.scheduledAt,
				},
			})

			w, req := newPublicViewRequest("proj-1", id.String())
			h.PublicView(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 for status=%s, body=%s", w.Code, tc.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "January update") {
				t.Errorf("body missing subject: %s", w.Body.String())
			}
		})
	}
}

// TestPublicViewWrongProjectWhileSending confirms the widened status window did
// not widen the project gate with it.
func TestPublicViewWrongProjectWhileSending(t *testing.T) {
	id := uuid.New()
	h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{
		id: {
			ID:         id,
			ProjectUID: "proj-1",
			Subject:    "January update",
			BodyHTML:   "<p>Hello</p>",
			Status:     model.StatusSending,
		},
	})

	w, req := newPublicViewRequest("proj-2", id.String())
	h.PublicView(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestPublicViewMalformedUUID(t *testing.T) {
	h := newPublicViewHandler(map[uuid.UUID]*model.Newsletter{})

	w, req := newPublicViewRequest("proj-1", "not-a-uuid")
	h.PublicView(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

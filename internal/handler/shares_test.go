// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

type handlerShareNewsletters map[uuid.UUID]*model.Newsletter

func (f handlerShareNewsletters) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	if n, ok := f[id]; ok {
		return n, nil
	}
	return nil, domain.ErrNotFound
}

type handlerShareRepo struct {
	recorded int
	stats    *model.ShareStats
	err      error
}

func (f *handlerShareRepo) RecordShare(context.Context, *model.NewsletterShare) error {
	f.recorded++
	return nil
}

func (f *handlerShareRepo) ShareCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		out[id] = 2
	}
	return out, nil
}

func (f *handlerShareRepo) DeleteSharesByUser(context.Context, string) (int64, error) {
	return 0, nil
}

func (f *handlerShareRepo) ShareStats(context.Context, uuid.UUID) (*model.ShareStats, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.stats, nil
}

func TestShareNewsletter(t *testing.T) {
	sentID, draftID := uuid.New(), uuid.New()
	newsletters := handlerShareNewsletters{
		sentID:  {ID: sentID, ProjectUID: "p1", Status: model.StatusSent},
		draftID: {ID: draftID, ProjectUID: "p1", Status: model.StatusDraft},
	}

	tests := []struct {
		name       string
		id         string
		user       string
		body       string
		wantStatus int
		wantStored int
	}{
		{"records a share", sentID.String(), "alice", `{"channel":"linkedin"}`, http.StatusNoContent, 1},
		{"unknown channel", sentID.String(), "alice", `{"channel":"myspace"}`, http.StatusBadRequest, 0},
		{"unknown field", sentID.String(), "alice", `{"channel":"x","extra":1}`, http.StatusBadRequest, 0},
		{"no principal", sentID.String(), "", `{"channel":"x"}`, http.StatusBadRequest, 0},
		{"draft", draftID.String(), "alice", `{"channel":"x"}`, http.StatusNotFound, 0},
		{"bad id", "not-a-uuid", "alice", `{"channel":"x"}`, http.StatusBadRequest, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &handlerShareRepo{}
			h := New(Config{Shares: service.NewShareService(newsletters, repo)})
			req := httptest.NewRequest(http.MethodPost, "/projects/p1/newsletters/"+tc.id+"/shares", strings.NewReader(tc.body))
			req.SetPathValue("project_uid", "p1")
			req.SetPathValue("newsletter_uid", tc.id)
			if tc.user != "" {
				req = req.WithContext(context.WithValue(req.Context(), userContextKeyValue, tc.user))
			}
			rec := httptest.NewRecorder()
			h.ShareNewsletter(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if repo.recorded != tc.wantStored {
				t.Errorf("recorded = %d, want %d", repo.recorded, tc.wantStored)
			}
		})
	}
}

func TestHandlerRoutes_Shares(t *testing.T) {
	path := "/projects/p1/newsletters/" + uuid.NewString() + "/shares"

	t.Run("absent without a share service", func(t *testing.T) {
		rec := httptest.NewRecorder()
		New(Config{}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"channel":"x"}`)))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 404/405 when no ShareService is wired", rec.Code)
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		srv := New(Config{RequireUserAuth: true, Shares: service.NewShareService(handlerShareNewsletters{}, &handlerShareRepo{})}).Routes()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"channel":"x"}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 without a bearer", rec.Code)
		}
	})
}

func TestShareCountsAreFailSoft(t *testing.T) {
	sentID, draftID := uuid.New(), uuid.New()
	rows := []*model.Newsletter{{ID: sentID, Status: model.StatusSent}, {ID: draftID, Status: model.StatusDraft}}

	h := New(Config{Shares: service.NewShareService(handlerShareNewsletters{}, &handlerShareRepo{})})
	counts := h.shareCounts(context.Background(), rows)
	if got := countFor(counts, sentID); got == nil || *got != 2 {
		t.Errorf("sent share count = %v, want 2", got)
	}
	if got := countFor(counts, draftID); got != nil {
		t.Errorf("draft share count = %v, want nil (omitted)", *got)
	}

	failing := New(Config{Shares: service.NewShareService(handlerShareNewsletters{}, &handlerShareRepo{err: errors.New("db down")})})
	if got := countFor(failing.shareCounts(context.Background(), rows), sentID); got != nil {
		t.Errorf("share count on read failure = %v, want nil (omitted)", *got)
	}

	// The committee list DTO omits share_count when it's unknown.
	item := toAPICommitteeNewsletter(rows[0])
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "share_count") {
		t.Errorf("committee item without a count marshals share_count: %s", raw)
	}
}

func TestApplyShareStats(t *testing.T) {
	id := uuid.New()
	repo := &handlerShareRepo{stats: &model.ShareStats{Total: 3, ByChannel: map[model.ShareChannel]int{model.ShareChannelLinkedIn: 2, model.ShareChannelCopyLink: 1}}}
	h := New(Config{Shares: service.NewShareService(handlerShareNewsletters{}, repo)})

	var dto publicapi.NewsletterAnalytics
	h.applyShareStats(context.Background(), id, &dto)
	if dto.TotalShares == nil || *dto.TotalShares != 3 {
		t.Fatalf("total_shares = %v, want 3", dto.TotalShares)
	}
	want := map[string]int{"x": 0, "linkedin": 2, "reddit": 0, "email": 0, "copy_link": 1}
	for channel, count := range want {
		if dto.SharesByChannel[channel] != count {
			t.Errorf("shares_by_channel[%s] = %d, want %d", channel, dto.SharesByChannel[channel], count)
		}
	}

	repo.err = errors.New("db down")
	var failed publicapi.NewsletterAnalytics
	h.applyShareStats(context.Background(), id, &failed)
	if failed.TotalShares != nil || failed.SharesByChannel != nil {
		t.Error("share stats set on read failure, want omitted")
	}
}

func TestTotalOpensOmittedWithoutAnalytics(t *testing.T) {
	h := New(Config{})
	if got := h.totalOpens(context.Background(), &model.Newsletter{ID: uuid.New(), Status: model.StatusSent}); got != nil {
		t.Errorf("total opens without an analytics service = %d, want nil (omitted)", *got)
	}

	// An unknown open total stays out of the JSON rather than reading as zero.
	raw, _ := json.Marshal(publicapi.Newsletter{})
	if strings.Contains(string(raw), "total_opens") {
		t.Errorf("newsletter without opens marshals total_opens: %s", raw)
	}
}

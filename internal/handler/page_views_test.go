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

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

type handlerPageViewRepo struct {
	recorded int
	err      error
}

func (f *handlerPageViewRepo) RecordPageView(context.Context, *model.NewsletterPageView) error {
	f.recorded++
	return nil
}

func (f *handlerPageViewRepo) PageViewCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		out[id] = 4
	}
	return out, nil
}

func (f *handlerPageViewRepo) DeletePageViewsByUser(context.Context, string) (int64, error) {
	return 0, nil
}

func TestRecordPageView(t *testing.T) {
	sentID, draftID := uuid.New(), uuid.New()
	newsletters := handlerShareNewsletters{
		sentID:  {ID: sentID, ProjectUID: "p1", Status: model.StatusSent},
		draftID: {ID: draftID, ProjectUID: "p1", Status: model.StatusDraft},
	}

	tests := []struct {
		name       string
		project    string
		id         string
		user       string
		wantStatus int
		wantStored int
	}{
		{"records a view", "p1", sentID.String(), "alice", http.StatusNoContent, 1},
		{"no principal", "p1", sentID.String(), "", http.StatusBadRequest, 0},
		{"anonymous principal", "p1", sentID.String(), service.AnonymousPrincipal, http.StatusBadRequest, 0},
		{"draft", "p1", draftID.String(), "alice", http.StatusNotFound, 0},
		{"other project", "p2", sentID.String(), "alice", http.StatusNotFound, 0},
		{"bad id", "p1", "not-a-uuid", "alice", http.StatusBadRequest, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &handlerPageViewRepo{}
			h := New(Config{PageViews: service.NewPageViewService(newsletters, repo)})
			req := httptest.NewRequest(http.MethodPost, "/projects/"+tc.project+"/newsletters/"+tc.id+"/page-views", nil)
			req.SetPathValue("project_uid", tc.project)
			req.SetPathValue("newsletter_uid", tc.id)
			if tc.user != "" {
				req = req.WithContext(context.WithValue(req.Context(), userContextKeyValue, tc.user))
			}
			rec := httptest.NewRecorder()
			h.RecordPageView(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if repo.recorded != tc.wantStored {
				t.Errorf("recorded = %d, want %d", repo.recorded, tc.wantStored)
			}
		})
	}
}

func TestHandlerRoutes_PageViews(t *testing.T) {
	path := "/projects/p1/newsletters/" + uuid.NewString() + "/page-views"

	t.Run("absent without a page-view service", func(t *testing.T) {
		rec := httptest.NewRecorder()
		New(Config{}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 404/405 when no PageViewService is wired", rec.Code)
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		srv := New(Config{RequireUserAuth: true, PageViews: service.NewPageViewService(handlerShareNewsletters{}, &handlerPageViewRepo{})}).Routes()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 without a bearer", rec.Code)
		}
	})
}

func TestPageViewCountsAreFailSoft(t *testing.T) {
	sentID, draftID := uuid.New(), uuid.New()
	rows := []*model.Newsletter{{ID: sentID, Status: model.StatusSent}, {ID: draftID, Status: model.StatusDraft}}

	h := New(Config{PageViews: service.NewPageViewService(handlerShareNewsletters{}, &handlerPageViewRepo{})})
	counts := h.pageViewCounts(context.Background(), rows)
	if got := shareCountFor(counts, sentID); got == nil || *got != 4 {
		t.Errorf("sent page-view count = %v, want 4", got)
	}
	if got := shareCountFor(counts, draftID); got != nil {
		t.Errorf("draft page-view count = %v, want nil (omitted)", *got)
	}

	failing := New(Config{PageViews: service.NewPageViewService(handlerShareNewsletters{}, &handlerPageViewRepo{err: errors.New("db down")})})
	if got := shareCountFor(failing.pageViewCounts(context.Background(), rows), sentID); got != nil {
		t.Errorf("page-view count on read failure = %v, want nil (omitted)", *got)
	}
	if got := New(Config{}).pageViewCounts(context.Background(), rows); got != nil {
		t.Errorf("page-view counts without a service = %v, want nil", got)
	}

	// Unknown counts stay out of the JSON rather than reading as zero.
	for name, v := range map[string]any{
		"newsletter":           publicapi.Newsletter{},
		"committee newsletter": toAPICommitteeNewsletter(rows[0]),
		"analytics":            publicapi.NewsletterAnalytics{},
	} {
		raw, _ := json.Marshal(v)
		for _, field := range []string{"page_view_count", "total_page_views", "total_opens\":null"} {
			if strings.Contains(string(raw), field) {
				t.Errorf("%s without counts marshals %s: %s", name, field, raw)
			}
		}
	}
}

func TestTotalOpensForWithoutAnalytics(t *testing.T) {
	rows := []*model.Newsletter{{ID: uuid.New(), Status: model.StatusSent}, {ID: uuid.New(), Status: model.StatusDraft}}
	got := New(Config{}).totalOpensFor(context.Background(), rows)
	if len(got) != len(rows) {
		t.Fatalf("len = %d, want %d (index-aligned)", len(got), len(rows))
	}
	for i, v := range got {
		if v != nil {
			t.Errorf("row %d total opens = %d, want nil without an analytics service", i, *v)
		}
	}
}

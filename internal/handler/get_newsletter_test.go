// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// getOneRepo serves one newsletter and its local analytics row. Embedding the
// interface leaves every method GET one never calls unimplemented.
type getOneRepo struct {
	port.NewsletterRepository
	newsletter   *model.Newsletter
	localOpens   int
	analyticsErr error
}

func (r *getOneRepo) Get(_ context.Context, _ uuid.UUID) (*model.Newsletter, error) {
	return r.newsletter, nil
}

func (r *getOneRepo) OpenCount(_ context.Context, _ uuid.UUID) (int, error) {
	if r.analyticsErr != nil {
		return 0, r.analyticsErr
	}
	return r.localOpens, nil
}

// getOneReader reports the provider's scalar open total.
type getOneReader struct {
	port.EngagementReader
	opened int
}

func (r *getOneReader) GetEngagement(_ context.Context, _ string) (*port.EmailEngagement, error) {
	return &port.EmailEngagement{Opened: r.opened}, nil
}

// TestGetNewsletterTotalOpens drives GET one through the handler: total_opens is
// in the body for a sent newsletter, absent for a draft, and absent — with the
// read still 200 and the ETag set — when the local analytics read fails.
func TestGetNewsletterTotalOpens(t *testing.T) {
	const projectUID = "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	groupID := "group-xyz"

	cases := []struct {
		name         string
		status       model.Status
		analyticsErr error
		wantOpens    *int
	}{
		{name: "sent", status: model.StatusSent, wantOpens: intPtr(7)},
		{name: "draft", status: model.StatusDraft},
		{name: "local read failure", status: model.StatusSent, analyticsErr: errors.New("db down")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &model.Newsletter{ID: uuid.New(), ProjectUID: projectUID, Status: tc.status, GroupID: &groupID, Version: 3}
			repo := &getOneRepo{newsletter: n, localOpens: 2, analyticsErr: tc.analyticsErr}
			readers := map[string]port.EngagementReader{model.SendProviderEmailService: &getOneReader{opened: 7}}
			h := New(Config{
				Newsletter: service.NewNewsletterService(repo),
				Analytics:  service.NewAnalyticsService(repo, readers, nil, model.SendProviderEmailService),
			})

			req := httptest.NewRequest(http.MethodGet, "/projects/"+projectUID+"/newsletters/"+n.ID.String(), nil)
			req.SetPathValue("project_uid", projectUID)
			req.SetPathValue("newsletter_uid", n.ID.String())
			rec := httptest.NewRecorder()
			h.GetNewsletter(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("ETag"); got != `"3"` {
				t.Errorf("ETag = %q, want %q", got, `"3"`)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			got, present := body["total_opens"]
			if tc.wantOpens == nil {
				if present {
					t.Errorf("total_opens = %v, want omitted", got)
				}
				return
			}
			if !present || got != float64(*tc.wantOpens) {
				t.Errorf("total_opens = %v (present %v), want %d", got, present, *tc.wantOpens)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

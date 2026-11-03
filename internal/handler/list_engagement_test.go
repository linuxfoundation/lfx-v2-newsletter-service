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
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// engagementRepo serves a committee list page plus per-newsletter local open
// counts and analytics rows. Embedding the interface leaves every other method
// unimplemented.
type engagementRepo struct {
	port.NewsletterRepository
	page       []*model.Newsletter
	localOpens map[uuid.UUID]int
	openErr    error
	openCalls  int
}

func (r *engagementRepo) ListSentByCommittee(context.Context, string, string) (*port.ListPage, error) {
	return &port.ListPage{Newsletters: r.page}, nil
}

func (r *engagementRepo) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	for _, n := range r.page {
		if n.ID == id {
			return n, nil
		}
	}
	return nil, errors.New("not seeded")
}

func (r *engagementRepo) GetMeta(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return r.Get(ctx, id)
}

func (r *engagementRepo) OpenCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	r.openCalls++
	if r.openErr != nil {
		return nil, r.openErr
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if c, ok := r.localOpens[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (r *engagementRepo) Analytics(_ context.Context, id uuid.UUID) (*model.Analytics, error) {
	n, err := r.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return &model.Analytics{NewsletterID: n.ID, Status: n.Status}, nil
}

// groupReader reports provider opens per group id; a group listed in hang
// blocks until the caller's context is done.
type groupReader struct {
	port.EngagementReader
	opened map[string]int
	hang   map[string]bool
}

func (r *groupReader) GetEngagement(ctx context.Context, groupID string) (*port.EmailEngagement, error) {
	if r.hang[groupID] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &port.EmailEngagement{Opened: r.opened[groupID]}, nil
}

func sentWithGroup(group string) *model.Newsletter {
	g := group
	return &model.Newsletter{ID: uuid.New(), ProjectUID: "p1", Status: model.StatusSent, GroupID: &g}
}

// listCommitteePage runs ListCommitteeNewsletters against repo and returns the
// rows keyed by id plus how long the call took.
func listCommitteePage(t *testing.T, repo *engagementRepo, reader port.EngagementReader) (map[string]map[string]any, time.Duration) {
	t.Helper()
	h := New(Config{
		Newsletter: service.NewNewsletterService(repo, true),
		Analytics:  service.NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: reader}, nil, model.SendProviderEmailService),
		PageViews:  service.NewPageViewService(handlerShareNewsletters{}, &handlerPageViewRepo{}),
	})
	req := httptest.NewRequest(http.MethodGet, "/committees/c1/newsletters", nil)
	req.SetPathValue("committee_uid", "c1")
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ListCommitteeNewsletters(rec, req)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Newsletters []map[string]any `json:"newsletters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	rows := map[string]map[string]any{}
	for _, row := range body.Newsletters {
		rows[row["id"].(string)] = row
	}
	return rows, elapsed
}

// TestListCommitteeNewslettersEngagement drives the committee list through the
// handler and checks each row's total_opens and page_view_count land on that
// row: provider opens win over local, a hung provider falls back to the local
// count inside the one shared budget, a sent row with no local opens and no
// provider data reads 0, and a draft row carries neither field. Local counts
// come from one grouped query for the whole page.
func TestListCommitteeNewslettersEngagement(t *testing.T) {
	providerWins := sentWithGroup("g-provider")
	hung := sentWithGroup("g-hung")
	quiet := sentWithGroup("g-quiet")
	draft := &model.Newsletter{ID: uuid.New(), ProjectUID: "p1", Status: model.StatusDraft}

	// More hung rows than fit in one concurrency wave: if each wave (or row)
	// got its own provider budget the page would take at least two budgets.
	page := []*model.Newsletter{providerWins, hung, quiet, draft}
	for i := 0; i < 20; i++ {
		page = append(page, sentWithGroup("g-hung"))
	}

	repo := &engagementRepo{page: page, localOpens: map[uuid.UUID]int{providerWins.ID: 2, hung.ID: 5}}
	reader := &groupReader{opened: map[string]int{"g-provider": 40}, hang: map[string]bool{"g-hung": true}}
	rows, elapsed := listCommitteePage(t, repo, reader)

	// Anything under two budgets proves the budget is shared; the slack absorbs
	// -race and a busy CI runner.
	if limit := 2*totalOpensListBudget - 100*time.Millisecond; elapsed > limit {
		t.Errorf("list took %v with hung provider reads, want under %v (one shared budget)", elapsed, limit)
	}
	if repo.openCalls != 1 {
		t.Errorf("local open-count queries = %d, want 1 grouped query for the page", repo.openCalls)
	}

	expect := []struct {
		name      string
		id        uuid.UUID
		wantOpens any // float64 value, or nil for omitted
		wantViews any
	}{
		{"provider opens win", providerWins.ID, float64(40), float64(4)},
		{"hung provider falls back to local", hung.ID, float64(5), float64(4)},
		{"no opens anywhere reads zero", quiet.ID, float64(0), float64(4)},
		{"draft carries neither", draft.ID, nil, nil},
	}
	for _, tc := range expect {
		row := rows[tc.id.String()]
		if row == nil {
			t.Fatalf("%s: row %s missing from response", tc.name, tc.id)
		}
		if got := row["total_opens"]; got != tc.wantOpens {
			t.Errorf("%s: total_opens = %v, want %v", tc.name, got, tc.wantOpens)
		}
		if got := row["page_view_count"]; got != tc.wantViews {
			t.Errorf("%s: page_view_count = %v, want %v", tc.name, got, tc.wantViews)
		}
	}
}

// TestListCommitteeNewslettersOpensReadFailure: a failed local open-count read
// omits total_opens on every row but keeps the list (and page views) intact.
func TestListCommitteeNewslettersOpensReadFailure(t *testing.T) {
	sent := sentWithGroup("g-provider")
	repo := &engagementRepo{page: []*model.Newsletter{sent}, openErr: errors.New("db down")}
	rows, _ := listCommitteePage(t, repo, &groupReader{opened: map[string]int{"g-provider": 40}})

	row := rows[sent.ID.String()]
	if row == nil {
		t.Fatal("row missing from response")
	}
	if got, present := row["total_opens"]; present {
		t.Errorf("total_opens = %v, want omitted on a local read failure", got)
	}
	if got := row["page_view_count"]; got != float64(4) {
		t.Errorf("page_view_count = %v, want 4", got)
	}
}

// TestGetAnalyticsTotalPageViews checks total_page_views is set for a sent
// newsletter and omitted for a draft.
func TestGetAnalyticsTotalPageViews(t *testing.T) {
	sent := &model.Newsletter{ID: uuid.New(), ProjectUID: "p1", Status: model.StatusSent}
	draft := &model.Newsletter{ID: uuid.New(), ProjectUID: "p1", Status: model.StatusDraft}
	repo := &engagementRepo{page: []*model.Newsletter{sent, draft}}
	h := New(Config{
		Analytics: service.NewAnalyticsService(repo, map[string]port.EngagementReader{}, nil, model.SendProviderEmailService),
		PageViews: service.NewPageViewService(handlerShareNewsletters{}, &handlerPageViewRepo{}),
	})

	for _, tc := range []struct {
		name string
		n    *model.Newsletter
		want any
	}{
		{"sent", sent, float64(4)},
		{"draft", draft, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/projects/p1/newsletters/"+tc.n.ID.String()+"/analytics", nil)
			req.SetPathValue("project_uid", "p1")
			req.SetPathValue("newsletter_uid", tc.n.ID.String())
			rec := httptest.NewRecorder()
			h.GetAnalytics(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got := body["total_page_views"]; got != tc.want {
				t.Errorf("total_page_views = %v, want %v", got, tc.want)
			}
		})
	}
}

// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

const testProject = "p1"

type audienceStubRepo struct {
	mu        sync.Mutex
	audiences map[uuid.UUID]*model.Audience
	members   map[uuid.UUID][]model.CommitteeMember
}

func newAudienceStubRepo() *audienceStubRepo {
	return &audienceStubRepo{
		audiences: map[uuid.UUID]*model.Audience{},
		members:   map[uuid.UUID][]model.CommitteeMember{},
	}
}

func (r *audienceStubRepo) BeginResolve(_ context.Context, id uuid.UUID, projectUID string, uids []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.audiences[id]; ok && a.Status == model.AudienceProcessing {
		return false, nil
	}
	r.audiences[id] = &model.Audience{NewsletterID: id, ProjectUID: projectUID, CommitteeUIDs: uids, Status: model.AudienceProcessing, RequestedAt: time.Now()}
	return true, nil
}

func (r *audienceStubRepo) ReplaceMembers(_ context.Context, id uuid.UUID, m []model.CommitteeMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[id] = m
	return nil
}

func (r *audienceStubRepo) MarkReady(_ context.Context, id uuid.UUID, count, skipped int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	a := r.audiences[id]
	a.Status, a.RecipientCount, a.SkippedNoEmail, a.ResolvedAt = model.AudienceReady, count, skipped, &now
	return nil
}

func (r *audienceStubRepo) MarkFailed(_ context.Context, id uuid.UUID, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.audiences[id]
	a.Status, a.Error = model.AudienceFailed, msg
	return nil
}

func (r *audienceStubRepo) Get(_ context.Context, id uuid.UUID) (*model.Audience, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.audiences[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *audienceStubRepo) ListMembers(_ context.Context, id uuid.UUID) ([]model.CommitteeMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.members[id], nil
}

func (r *audienceStubRepo) FailStale(context.Context, time.Duration) (int, error) { return 0, nil }

type newsletterStub struct{ n *model.Newsletter }

func (s newsletterStub) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	if s.n == nil || s.n.ID != id {
		return nil, domain.ErrNotFound
	}
	return s.n, nil
}

type noUnsubscribes struct{}

func (noUnsubscribes) ListUnsubscribedEmails(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// committeeStub serves a fixed member list and records the committees asked for.
type committeeStub struct {
	mu      sync.Mutex
	members []model.CommitteeMember
	err     error
	calls   []string
}

func (c *committeeStub) ListMembers(_ context.Context, uid string) (model.CommitteeMembers, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, uid)
	if c.err != nil {
		return model.CommitteeMembers{}, c.err
	}
	return model.CommitteeMembers{Members: c.members}, nil
}

type audienceHarness struct {
	srv        http.Handler
	svc        *service.AudienceService
	audiences  *audienceStubRepo
	committee  *committeeStub
	newsletter *model.Newsletter
}

func newAudienceHarness(t *testing.T) *audienceHarness {
	t.Helper()
	n := &model.Newsletter{ID: uuid.New(), ProjectUID: testProject, Status: model.StatusDraft, CommitteeUIDs: []string{"c1"}}
	audiences := newAudienceStubRepo()
	committee := &committeeStub{members: []model.CommitteeMember{{Email: "first.last@example.com", FirstName: "First", LastName: "Last"}}}
	svc := service.NewAudienceService(service.AudienceServiceConfig{
		Newsletters:  newsletterStub{n: n},
		Audiences:    audiences,
		Committee:    committee,
		Unsubscribed: noUnsubscribes{},
		JobTimeout:   time.Minute,
	})
	// RequireUserAuth is off so the tests reach the handlers without a JWT.
	srv := New(Config{Audience: svc, RequireUserAuth: false}).Routes()
	return &audienceHarness{srv: srv, svc: svc, audiences: audiences, committee: committee, newsletter: n}
}

func (h *audienceHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func (h *audienceHarness) path() string {
	return "/projects/" + testProject + "/newsletters/" + h.newsletter.ID.String() + "/audience"
}

func TestResolveAudience_Returns202ThenGetShowsReady(t *testing.T) {
	h := newAudienceHarness(t)

	rec := h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var created struct {
		Status        string   `json:"status"`
		CommitteeUIDs []string `json:"committee_uids"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "processing", created.Status)
	assert.Equal(t, []string{"c1"}, created.CommitteeUIDs)

	h.svc.Drain(context.Background())

	rec = h.do(http.MethodGet, h.path(), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "ready", got["status"])
	assert.EqualValues(t, 1, got["recipient_count"])
	assert.EqualValues(t, 0, got["skipped_no_email"])
	assert.NotEmpty(t, got["requested_at"])
	assert.NotEmpty(t, got["resolved_at"])
	assert.NotContains(t, got, "error", "no error on a ready audience")
	assert.NotContains(t, rec.Body.String(), "first.last@example.com", "the audience status does not list emails")
	assert.Equal(t, []string{"c1"}, h.committee.calls)
}

func TestResolveAudience_Returns200WhenAlreadyReadyForSameCommittees(t *testing.T) {
	h := newAudienceHarness(t)
	require.Equal(t, http.StatusAccepted, h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`).Code)
	h.svc.Drain(context.Background())

	rec := h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"c1"}, h.committee.calls, "the committee is not read again")
}

func TestResolveAudience_Returns202WhileProcessing(t *testing.T) {
	h := newAudienceHarness(t)
	_, err := h.audiences.BeginResolve(context.Background(), h.newsletter.ID, testProject, []string{"c1"})
	require.NoError(t, err)

	rec := h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`)
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status":"processing"`)
	assert.Empty(t, h.committee.calls, "an in-flight resolution is not started twice")
}

func TestGetAudience_Returns404WhenNeverResolved(t *testing.T) {
	h := newAudienceHarness(t)
	rec := h.do(http.MethodGet, h.path(), "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetAudience_FailedShowsError(t *testing.T) {
	h := newAudienceHarness(t)
	h.committee.err = pkgerrors.NewNotFound("committee not found")
	require.Equal(t, http.StatusAccepted, h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`).Code)
	h.svc.Drain(context.Background())

	rec := h.do(http.MethodGet, h.path(), "")
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "failed", got["status"])
	assert.Equal(t, "a selected committee was not found", got["error"])
}

func TestAudienceRoutes_Validation(t *testing.T) {
	h := newAudienceHarness(t)
	base := "/projects/" + testProject + "/newsletters/"

	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"bad newsletter uuid on POST", http.MethodPost, base + "nope/audience", `{"committee_uids":["c1"]}`, http.StatusBadRequest},
		{"bad newsletter uuid on GET", http.MethodGet, base + "nope/audience", "", http.StatusBadRequest},
		{"missing body", http.MethodPost, h.path(), "", http.StatusBadRequest},
		{"unknown field", http.MethodPost, h.path(), `{"committee_uids":["c1"],"extra":1}`, http.StatusBadRequest},
		{"no committees", http.MethodPost, h.path(), `{"committee_uids":[]}`, http.StatusBadRequest},
		{"unknown newsletter", http.MethodPost, base + uuid.NewString() + "/audience", `{"committee_uids":["c1"]}`, http.StatusNotFound},
		{"another project", http.MethodPost, "/projects/other/newsletters/" + h.newsletter.ID.String() + "/audience", `{"committee_uids":["c1"]}`, http.StatusNotFound},
		{"GET from another project", http.MethodGet, "/projects/other/newsletters/" + h.newsletter.ID.String() + "/audience", "", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(tc.method, tc.path, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
	assert.Empty(t, h.committee.calls)
}

func TestResolveAudience_RejectsNewslettersThatAreNotDrafts(t *testing.T) {
	h := newAudienceHarness(t)
	h.newsletter.Status = model.StatusSending

	rec := h.do(http.MethodPost, h.path(), `{"committee_uids":["c1"]}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "send_in_progress")
}

func TestAudienceRoutes_RequireAuth(t *testing.T) {
	srv := New(Config{RequireUserAuth: true}).Routes()
	path := "/projects/" + testProject + "/newsletters/" + uuid.NewString() + "/audience"
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"committee_uids":["c1"]}`)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, method)
	}
}

func TestWriteError_AudienceNotReadyIs409WithStatus(t *testing.T) {
	for _, status := range []string{domain.AudienceStateNone, "processing", "failed", domain.AudienceStateStale} {
		t.Run(status, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(context.Background(), rec, &domain.AudienceNotReadyError{Status: status})

			require.Equal(t, http.StatusConflict, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, "audience_not_ready", body["error"])
			assert.Equal(t, status, body["audience_status"])
		})
	}
}

func TestWriteError_OtherErrorsHaveNoAudienceStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(context.Background(), rec, domain.ErrNotFound)
	assert.NotContains(t, rec.Body.String(), "audience_status")
}

func TestRecipientRoutes_RejectBadNewsletterUID(t *testing.T) {
	h := newAudienceHarness(t)
	base := "/projects/" + testProject + "/newsletters/"
	for _, route := range []string{"recipient-count", "recipients"} {
		t.Run(route, func(t *testing.T) {
			rec := h.do(http.MethodPost, base+route, `{"committee_uids":["c1"]}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "newsletter_uid is required: %s", rec.Body.String())
			rec = h.do(http.MethodPost, base+route, `{"newsletter_uid":"nope","committee_uids":["c1"]}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestToAPIAudience_NilCommitteesBecomeEmptyArray(t *testing.T) {
	dto := toAPIAudience(&model.Audience{Status: model.AudienceProcessing})
	body, err := json.Marshal(dto)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"committee_uids":[]`)
	assert.NotContains(t, string(body), "resolved_at")
}

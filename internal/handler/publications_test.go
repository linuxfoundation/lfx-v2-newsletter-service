// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

type handlerPublicationRepo struct {
	pub *model.Publication
}

func (r *handlerPublicationRepo) CreatePublication(_ context.Context, p *model.Publication) error {
	p.ID, p.Version = uuid.New(), 1
	return nil
}

func (r *handlerPublicationRepo) GetPublication(_ context.Context, projectUID string, id uuid.UUID) (*model.Publication, error) {
	if r.pub == nil || r.pub.ID != id || r.pub.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return r.pub, nil
}

func (r *handlerPublicationRepo) ListPublications(_ context.Context, _, _ string) (*port.PublicationListPage, error) {
	if r.pub == nil {
		return &port.PublicationListPage{}, nil
	}
	return &port.PublicationListPage{Publications: []*model.Publication{r.pub}, NextPageToken: "next"}, nil
}

func (r *handlerPublicationRepo) UpdatePublication(_ context.Context, p *model.Publication, expected int64) (*model.Publication, error) {
	if expected != p.Version {
		return nil, domain.ErrVersionMismatch
	}
	p.Version++
	return p, nil
}

func publicationsHandler(pub *model.Publication) *Handler {
	return &Handler{publications: service.NewPublicationService(&handlerPublicationRepo{pub: pub}, nil)}
}

func doJSON(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestPublicationRoutesRequireAuth(t *testing.T) {
	h := &Handler{requireUserAuth: true, publications: service.NewPublicationService(&handlerPublicationRepo{}, nil)}
	routes := h.Routes()
	cases := []struct{ method, path string }{
		{http.MethodPost, "/projects/p1/newsletter-publications"},
		{http.MethodGet, "/projects/p1/newsletter-publications"},
		{http.MethodGet, "/projects/p1/newsletter-publications/" + uuid.NewString()},
		{http.MethodPut, "/projects/p1/newsletter-publications/" + uuid.NewString()},
		{http.MethodGet, "/projects/p1/newsletter-publications/" + uuid.NewString() + "/editions"},
		{http.MethodPost, "/projects/p1/newsletter-publications/" + uuid.NewString() + "/editions"},
	}
	for _, tc := range cases {
		if w := doJSON(routes, tc.method, tc.path, "{}", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestCreatePublicationHandler(t *testing.T) {
	routes := publicationsHandler(nil).Routes()
	w := doJSON(routes, http.MethodPost, "/projects/p1/newsletter-publications",
		`{"name":"Weekly","template_key":"aaif-member","reply_to_email":"weekly@example.com","committee_uids":["c1"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body)
	}
	if w.Header().Get("ETag") != `"1"` {
		t.Errorf("ETag = %q", w.Header().Get("ETag"))
	}
	var got publicapi.Publication
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectUID != "p1" || got.Name != "Weekly" || got.Implicit || *got.TemplateKey != "aaif-member" {
		t.Errorf("publication = %+v", got)
	}

	if w := doJSON(routes, http.MethodPost, "/projects/p1/newsletter-publications", `{"name":"x","bogus":1}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: status = %d, want 400", w.Code)
	}
	if w := doJSON(routes, http.MethodPost, "/projects/p1/newsletter-publications", `{"name":""}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("blank name: status = %d, want 400", w.Code)
	}
}

func TestGetAndUpdatePublicationHandler(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Name: "Weekly", Version: 3, CommitteeUIDs: []string{}}
	routes := publicationsHandler(pub).Routes()
	path := "/projects/p1/newsletter-publications/" + pub.ID.String()

	if w := doJSON(routes, http.MethodGet, path, "", nil); w.Code != http.StatusOK || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: status = %d ETag %q", w.Code, w.Header().Get("ETag"))
	}
	if w := doJSON(routes, http.MethodGet, "/projects/p2/newsletter-publications/"+pub.ID.String(), "", nil); w.Code != http.StatusNotFound {
		t.Errorf("get in another project: status = %d, want 404", w.Code)
	}
	if w := doJSON(routes, http.MethodGet, "/projects/p1/newsletter-publications/not-a-uuid", "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("malformed uuid: status = %d, want 400", w.Code)
	}
	if w := doJSON(routes, http.MethodPut, path, `{"name":"Renamed"}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("missing If-Match: status = %d, want 400", w.Code)
	}
	if w := doJSON(routes, http.MethodPut, path, `{"name":"Renamed"}`, map[string]string{"If-Match": `"2"`}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status = %d, want 412", w.Code)
	}
	if w := doJSON(routes, http.MethodPut, path, `{"name":"Renamed"}`, map[string]string{"If-Match": `"3"`}); w.Code != http.StatusOK || w.Header().Get("ETag") != `"4"` {
		t.Errorf("update: status = %d ETag %q", w.Code, w.Header().Get("ETag"))
	}
}

func TestListPublicationsHandler(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Name: "Weekly", CommitteeUIDs: []string{}}
	w := doJSON(publicationsHandler(pub).Routes(), http.MethodGet, "/projects/p1/newsletter-publications", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got publicapi.PublicationListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Publications) != 1 || got.NextPageToken != "next" {
		t.Errorf("list = %+v", got)
	}
}

func TestCreateEditionRejectsBodyPublicationMismatch(t *testing.T) {
	pubID := uuid.New()
	w := doJSON(publicationsHandler(&model.Publication{ID: pubID, ProjectUID: "p1"}).Routes(), http.MethodPost,
		"/projects/p1/newsletter-publications/"+pubID.String()+"/editions",
		`{"publication_uid":"`+uuid.NewString()+`","subject":"s","body_html":"<p>x</p>","ed_reply_email":"e@example.com","committee_uids":["c1"]}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body)
	}
}

func TestCreateNewsletterRejectsMalformedPublicationUID(t *testing.T) {
	w := doJSON(publicationsHandler(nil).Routes(), http.MethodPost, "/projects/p1/newsletters",
		`{"publication_uid":"not-a-uuid","subject":"s","body_html":"<p>x</p>","ed_reply_email":"e@example.com","committee_uids":["c1"]}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body)
	}
}

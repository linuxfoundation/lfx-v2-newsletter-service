// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/handler"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// newPublicationsAPI serves the real routes over the integration database.
// These route tests live here rather than in the handler package because `go
// test ./...` runs packages concurrently: every test that touches the shared
// integration database has to stay in this one package so they run serially
// (TestPublicationsBackfill rewinds the schema; newIntegrationRepo truncates).
func newPublicationsAPI(t *testing.T) http.Handler {
	t.Helper()
	repo := newIntegrationRepo(t)
	pubRepo := NewPostgresPublicationRepo(repo.db)
	newsletterSvc := service.NewNewsletterService(repo, true)
	newsletterSvc.UsePublications(pubRepo)
	return handler.New(handler.Config{Newsletter: newsletterSvc, Publications: service.NewPublicationService(pubRepo, repo)}).Routes()
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

func decodeBody[T any](t *testing.T, raw *strings.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(raw).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// TestPublicationRoutes_EndToEnd drives the milestone flow over HTTP and a
// real database: set up a publication, create an edition that inherits from
// it, list it, and confirm a client that sends no publication_uid still works.
func TestPublicationRoutes_EndToEnd(t *testing.T) {
	api := newPublicationsAPI(t)
	project := "project-" + uuid.NewString()
	base := "/projects/" + project + "/newsletter-publications"

	create := doJSON(api, http.MethodPost, base, `{"name":"AAIF Weekly","template_key":"aaif-member","from_name":"AAIF Weekly","reply_to_email":"weekly@example.com","committee_uids":["c1","c2"]}`, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("create publication: %d %s", create.Code, create.Body)
	}
	pub := decodeBody[publicapi.Publication](t, strings.NewReader(create.Body.String()))
	if pub.Implicit || pub.Version != 1 {
		t.Fatalf("publication = %+v", pub)
	}

	// Edition created under the publication: nothing but a subject supplied.
	edBase := base + "/" + pub.ID + "/editions"
	editionResp := doJSON(api, http.MethodPost, edBase, `{"subject":"This week"}`, nil)
	if editionResp.Code != http.StatusCreated {
		t.Fatalf("create edition: %d %s", editionResp.Code, editionResp.Body)
	}
	edition := decodeBody[publicapi.Newsletter](t, strings.NewReader(editionResp.Body.String()))
	if edition.PublicationUID == nil || *edition.PublicationUID != pub.ID {
		t.Fatalf("edition publication = %v", edition.PublicationUID)
	}
	if edition.EDReplyEmail != "weekly@example.com" || strings.Join(edition.CommitteeUIDs, ",") != "c1,c2" {
		t.Errorf("inherited reply=%q committees=%v", edition.EDReplyEmail, edition.CommitteeUIDs)
	}
	if edition.BodyLayout == nil || edition.BodyLayout.TemplateKey != "aaif-member" {
		t.Errorf("edition body_layout = %+v, want the publication's template set", edition.BodyLayout)
	}

	// The current create flow can name a publication too.
	viaCreate := doJSON(api, http.MethodPost, "/projects/"+project+"/newsletters",
		`{"subject":"Next week","publication_uid":"`+pub.ID+`","committee_uids":["c9"]}`, nil)
	if viaCreate.Code != http.StatusCreated {
		t.Fatalf("create with publication_uid: %d %s", viaCreate.Code, viaCreate.Body)
	}

	list := doJSON(api, http.MethodGet, edBase, "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list editions: %d %s", list.Code, list.Body)
	}
	editions := decodeBody[publicapi.NewsletterListResponse](t, strings.NewReader(list.Body.String()))
	if len(editions.Newsletters) != 2 {
		t.Fatalf("editions = %d, want 2", len(editions.Newsletters))
	}

	// A client that knows nothing about publications: exact payload today's UI
	// sends. Gets an implicit publication, no listing noise, no change.
	legacy := doJSON(api, http.MethodPost, "/projects/"+project+"/newsletters",
		`{"subject":"Legacy","body_html":"<p>x</p>","committee_uids":["c1"],"ed_reply_email":"ed@example.com","scheduled_at":null}`, nil)
	if legacy.Code != http.StatusCreated {
		t.Fatalf("legacy create: %d %s", legacy.Code, legacy.Body)
	}
	legacyNews := decodeBody[publicapi.Newsletter](t, strings.NewReader(legacy.Body.String()))
	if legacyNews.PublicationUID == nil || *legacyNews.PublicationUID == pub.ID {
		t.Fatalf("legacy publication_uid = %v", legacyNews.PublicationUID)
	}
	listAll := doJSON(api, http.MethodGet, base, "", nil)
	pubs := decodeBody[publicapi.PublicationListResponse](t, strings.NewReader(listAll.Body.String()))
	if len(pubs.Publications) != 1 || pubs.Publications[0].ID != pub.ID {
		t.Fatalf("publications listed = %+v, want only the named one", pubs.Publications)
	}

	// Update with If-Match.
	path := base + "/" + pub.ID
	if w := doJSON(api, http.MethodPut, path, `{"name":"Weekly renamed","template_key":"aaif-member","committee_uids":["c1"]}`, map[string]string{"If-Match": `"1"`}); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if w := doJSON(api, http.MethodPut, path, `{"name":"Stale"}`, map[string]string{"If-Match": `"1"`}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update: %d", w.Code)
	}
	get := doJSON(api, http.MethodGet, path, "", nil)
	got := decodeBody[publicapi.Publication](t, strings.NewReader(get.Body.String()))
	if got.Name != "Weekly renamed" || got.FromName != nil || got.Version != 2 {
		t.Fatalf("after update = %+v", got)
	}

	// Another project can't read it.
	if w := doJSON(api, http.MethodGet, "/projects/project-other/newsletter-publications/"+pub.ID, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross-project get: %d, want 404", w.Code)
	}
	if w := doJSON(api, http.MethodPost, "/projects/project-other/newsletters", `{"subject":"x","publication_uid":"`+pub.ID+`","body_html":"<p>x</p>","committee_uids":["c1"],"ed_reply_email":"e@example.com"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross-project edition: %d, want 404", w.Code)
	}
}

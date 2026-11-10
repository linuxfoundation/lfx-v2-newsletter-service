// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
)

const testTemplateKey = "aaif-member"

type fakePublicationRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*model.Publication
	err  error
}

func newFakePublicationRepo(pubs ...*model.Publication) *fakePublicationRepo {
	r := &fakePublicationRepo{rows: map[uuid.UUID]*model.Publication{}}
	for _, p := range pubs {
		r.rows[p.ID] = p
	}
	return r
}

func (r *fakePublicationRepo) CreatePublication(_ context.Context, p *model.Publication) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.ID, p.Version = uuid.New(), 1
	r.rows[p.ID] = p
	return r.err
}

func (r *fakePublicationRepo) GetPublication(_ context.Context, projectUID string, id uuid.UUID) (*model.Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	p, ok := r.rows[id]
	if !ok || p.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *fakePublicationRepo) ListPublications(context.Context, string, string) (*port.PublicationListPage, error) {
	return &port.PublicationListPage{}, nil
}

func (r *fakePublicationRepo) UpdatePublication(_ context.Context, p *model.Publication, _ int64) (*model.Publication, error) {
	return p, nil
}

func ptr[T any](v T) *T { return &v }

func weeklyPublication() *model.Publication {
	return &model.Publication{
		ID:            uuid.New(),
		ProjectUID:    "p1",
		Name:          "Weekly",
		TemplateKey:   ptr(testTemplateKey),
		ReplyToEmail:  ptr("weekly@example.com"),
		CommitteeUIDs: []string{"c1", "c2"},
	}
}

func publicationSvc(pubs ...*model.Publication) (*NewsletterService, *fakeNewsletterRepo) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)
	svc.UsePublications(newFakePublicationRepo(pubs...))
	return svc, repo
}

func storedTemplateKey(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var l declarative.Layout
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("unmarshal layout: %v", err)
	}
	return l.TemplateKey
}

func TestCreateDraftUnderPublicationInherits(t *testing.T) {
	pub := weeklyPublication()
	svc, _ := publicationSvc(pub)

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID: "p1", PublicationUID: &pub.ID, Subject: "This week", CreatedBy: "u1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.PublicationUID == nil || *draft.PublicationUID != pub.ID {
		t.Fatalf("PublicationUID = %v", draft.PublicationUID)
	}
	if draft.EDReplyEmail != "weekly@example.com" {
		t.Errorf("EDReplyEmail = %q, want the publication's", draft.EDReplyEmail)
	}
	if strings.Join(draft.CommitteeUIDs, ",") != "c1,c2" {
		t.Errorf("CommitteeUIDs = %v, want the publication's", draft.CommitteeUIDs)
	}
	if key := storedTemplateKey(t, draft.BodyLayout); key != testTemplateKey {
		t.Errorf("seeded layout template_key = %q", key)
	}
}

func TestCreateDraftExplicitValuesOverrideInherited(t *testing.T) {
	pub := weeklyPublication()
	svc, _ := publicationSvc(pub)

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:     "p1",
		PublicationUID: &pub.ID,
		Subject:        "This week",
		BodyLayout:     &declarative.Layout{Blocks: []declarative.Block{}},
		EDReplyEmail:   "other@example.com",
		CommitteeUIDs:  []string{"c9"},
		CreatedBy:      "u1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.EDReplyEmail != "other@example.com" || strings.Join(draft.CommitteeUIDs, ",") != "c9" {
		t.Errorf("reply=%q committees=%v", draft.EDReplyEmail, draft.CommitteeUIDs)
	}
	if key := storedTemplateKey(t, draft.BodyLayout); key != testTemplateKey {
		t.Errorf("blank template_key must inherit the publication's, got %q", key)
	}
}

func TestCreateDraftPublicationErrors(t *testing.T) {
	pub := weeklyPublication()
	bare := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Name: "No defaults", CommitteeUIDs: []string{}}
	other := weeklyPublication()
	other.ProjectUID = "p2"
	svc, _ := publicationSvc(pub, bare, other)

	unknown := uuid.New()
	cases := []struct {
		name    string
		in      CreateDraftInput
		wantErr error
	}{
		{"unknown publication", CreateDraftInput{PublicationUID: &unknown}, domain.ErrNotFound},
		{"publication in another project", CreateDraftInput{PublicationUID: &other.ID}, domain.ErrNotFound},
		{"body_html under a template set", CreateDraftInput{PublicationUID: &pub.ID, BodyHTML: "<p>x</p>"}, domain.ErrInvalidRequest},
		{"layout with another template", CreateDraftInput{PublicationUID: &pub.ID, BodyLayout: &declarative.Layout{TemplateKey: "aaif-weekly-agentic"}}, domain.ErrInvalidRequest},
		{"no audience anywhere", CreateDraftInput{PublicationUID: &bare.ID, BodyHTML: "<p>x</p>", EDReplyEmail: "e@example.com"}, domain.ErrInvalidRequest},
		{"explicitly empty audience does not inherit", CreateDraftInput{PublicationUID: &pub.ID, CommitteeUIDs: []string{}, EDReplyEmail: "e@example.com"}, domain.ErrInvalidRequest},
		{"no reply-to anywhere", CreateDraftInput{PublicationUID: &bare.ID, BodyHTML: "<p>x</p>", CommitteeUIDs: []string{"c1"}}, domain.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.ProjectUID, tc.in.Subject, tc.in.CreatedBy = "p1", "s", "u1"
			if _, err := svc.CreateDraft(context.Background(), tc.in); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A client that knows nothing about publications must keep working: no
// publication_uid means no lookup at all, even with none configured.
func TestCreateDraftWithoutPublicationNeedsNoLookup(t *testing.T) {
	svc := NewNewsletterService(newFakeRepo(), true)

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID: "p1", Subject: "s", BodyHTML: "<p>x</p>", EDReplyEmail: "e@example.com", CommitteeUIDs: []string{"c1"}, CreatedBy: "u1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.PublicationUID != nil {
		t.Fatalf("service must leave PublicationUID to the schema trigger, got %v", draft.PublicationUID)
	}
}

func TestUpdateDraftEnforcesPublicationTemplate(t *testing.T) {
	pinned := weeklyPublication()
	implicit := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Name: "Implicit", Implicit: true, CommitteeUIDs: []string{}}
	svc, repo := publicationSvc(pinned, implicit)

	addEdition := func(pubID uuid.UUID, layout string) *model.Newsletter {
		n := repo.addDraft("p1", []string{"c1"})
		repo.mu.Lock()
		defer repo.mu.Unlock()
		repo.drafts[n.ID].PublicationUID = &pubID
		repo.drafts[n.ID].BodyLayout = json.RawMessage(layout)
		return n
	}
	layoutWith := func(key string) *declarative.Layout {
		return &declarative.Layout{TemplateKey: key, Blocks: []declarative.Block{}}
	}
	update := func(n *model.Newsletter, set bool, l *declarative.Layout) error {
		_, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
			ID: n.ID, ExpectedVersion: n.Version, Subject: "s", BodyHTML: "<p>x</p>", BodyLayoutSet: set, BodyLayout: l,
			EDReplyEmail: "e@example.com", CommitteeUIDs: []string{"c1"},
		})
		return err
	}

	t.Run("matching template passes", func(t *testing.T) {
		n := addEdition(pinned.ID, `{"template_key":"aaif-member","blocks":[]}`)
		if err := update(n, true, layoutWith(testTemplateKey)); err != nil {
			t.Fatalf("update: %v", err)
		}
	})
	t.Run("different template rejected", func(t *testing.T) {
		n := addEdition(pinned.ID, `{"template_key":"aaif-member","blocks":[]}`)
		if err := update(n, true, layoutWith("aaif-weekly-agentic")); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("key the edition already has stays allowed", func(t *testing.T) {
		n := addEdition(pinned.ID, `{"template_key":"aaif-user-community","blocks":[]}`)
		if err := update(n, true, layoutWith("aaif-user-community")); err != nil {
			t.Fatalf("update: %v", err)
		}
	})
	t.Run("clearing the layout rejected", func(t *testing.T) {
		n := addEdition(pinned.ID, `{"template_key":"aaif-member","blocks":[]}`)
		if err := update(n, true, nil); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("implicit publication is unconstrained", func(t *testing.T) {
		n := addEdition(implicit.ID, `{"template_key":"aaif-member","blocks":[]}`)
		if err := update(n, true, layoutWith("aaif-weekly-agentic")); err != nil {
			t.Fatalf("update: %v", err)
		}
	})
}

func TestPublicationValidation(t *testing.T) {
	long := strings.Repeat("x", 201)
	cases := []struct {
		name string
		in   PublicationInput
	}{
		{"blank name", PublicationInput{Name: "  "}},
		{"long name", PublicationInput{Name: long}},
		{"unknown template", PublicationInput{Name: "n", TemplateKey: ptr("nope")}},
		{"long from name", PublicationInput{Name: "n", FromName: ptr(strings.Repeat("x", 101))}},
		{"control char in from name", PublicationInput{Name: "n", FromName: ptr("a\r\nb")}},
		{"bidi override in from name", PublicationInput{Name: "n", FromName: ptr("a\u202eb")}},
		{"zero-width space in from name", PublicationInput{Name: "n", FromName: ptr("a\u200bb")}},
		{"line separator in from name", PublicationInput{Name: "n", FromName: ptr("a\u2028b")}},
		{"from name over email-service byte limit", PublicationInput{Name: "n", FromName: ptr(strings.Repeat("\U0001F600", 65))}},
		{"reply-to not an address", PublicationInput{Name: "n", ReplyToEmail: ptr("not-an-email")}},
		{"reply-to with display name", PublicationInput{Name: "n", ReplyToEmail: ptr("Name <a@example.com>")}},
		{"reply-to on a domain not allowed", PublicationInput{Name: "n", ReplyToEmail: ptr("a@not-allowed.test")}},
		{"reply-to with a non-ASCII local part", PublicationInput{Name: "n", ReplyToEmail: ptr("jos\u00e9@example.com")}},
		{"reply-to local part over 64 bytes", PublicationInput{Name: "n", ReplyToEmail: ptr(strings.Repeat("a", 65) + "@example.com")}},
		{"name over 200 characters", PublicationInput{Name: strings.Repeat("é", 201)}},
		{"blank committee", PublicationInput{Name: "n", CommitteeUIDs: []string{" "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewPublicationService(newFakePublicationRepo(), newFakeRepo(), []string{"example.com"})
			if _, err := svc.CreatePublication(context.Background(), "p1", "u1", tc.in); !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestCreatePublicationNormalizes(t *testing.T) {
	svc := NewPublicationService(newFakePublicationRepo(), newFakeRepo(), []string{"example.com"})
	p, err := svc.CreatePublication(context.Background(), "p1", "u1", PublicationInput{
		Name:          "  Weekly ",
		TemplateKey:   ptr(" " + testTemplateKey),
		FromName:      ptr(" "),
		ReplyToEmail:  ptr(" weekly@example.com "),
		CommitteeUIDs: []string{" c1", "c1"},
	})
	if err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}
	if p.Name != "Weekly" || *p.TemplateKey != testTemplateKey || p.FromName != nil || *p.ReplyToEmail != "weekly@example.com" || strings.Join(p.CommitteeUIDs, ",") != "c1" {
		t.Fatalf("publication = %+v", p)
	}
}

// A multi-byte name is limited by characters, matching the database CHECK.
func TestCreatePublicationCountsNameInCharacters(t *testing.T) {
	svc := NewPublicationService(newFakePublicationRepo(), newFakeRepo(), nil)
	if _, err := svc.CreatePublication(context.Background(), "p1", "u1", PublicationInput{Name: strings.Repeat("é", 200)}); err != nil {
		t.Fatalf("200 characters must pass: %v", err)
	}
}

func TestImplicitPublicationIsNotWritable(t *testing.T) {
	implicit := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Name: "Implicit", Implicit: true, CommitteeUIDs: []string{"c1"}}

	svc := NewPublicationService(newFakePublicationRepo(implicit), newFakeRepo(), nil)
	if _, err := svc.UpdatePublication(context.Background(), "p1", implicit.ID, 1, PublicationInput{Name: "x", FromName: ptr("x")}); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("update: err = %v, want ErrInvalidRequest", err)
	}

	newsletters, _ := publicationSvc(implicit)
	_, err := newsletters.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID: "p1", PublicationUID: &implicit.ID, Subject: "s", BodyHTML: "<p>x</p>", EDReplyEmail: "e@example.com", CommitteeUIDs: []string{"c1"}, CreatedBy: "u1",
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("create edition: err = %v, want ErrInvalidRequest", err)
	}
}

// filterCapturingRepo records the filters ListEditions passes down.
type filterCapturingRepo struct {
	*fakeNewsletterRepo
	got port.ListFilters
}

func (r *filterCapturingRepo) ListAll(_ context.Context, f port.ListFilters) (*port.ListPage, error) {
	r.got = f
	return &port.ListPage{}, nil
}

func TestListEditionsForwardsProjectAndPublication(t *testing.T) {
	pub := weeklyPublication()
	repo := &filterCapturingRepo{fakeNewsletterRepo: newFakeRepo()}
	svc := NewPublicationService(newFakePublicationRepo(pub), repo, nil)
	if _, err := svc.ListEditions(context.Background(), ListEditionsInput{ProjectUID: "p1", PublicationUID: pub.ID, Status: model.StatusDraft}); err != nil {
		t.Fatalf("ListEditions: %v", err)
	}
	if repo.got.ProjectUID != "p1" || repo.got.PublicationUID == nil || *repo.got.PublicationUID != pub.ID || len(repo.got.Statuses) != 1 {
		t.Fatalf("filters = %+v", repo.got)
	}
}

func TestListEditionsScopesToProjectPublication(t *testing.T) {
	pub := weeklyPublication()
	svc := NewPublicationService(newFakePublicationRepo(pub), newFakeRepo(), nil)
	if _, err := svc.ListEditions(context.Background(), ListEditionsInput{ProjectUID: "p1", PublicationUID: uuid.New()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown publication: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.ListEditions(context.Background(), ListEditionsInput{ProjectUID: "p2", PublicationUID: pub.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other project: err = %v, want ErrNotFound", err)
	}
}

// A publication that gains a template set later must not strand an edition
// saved as basic HTML before that.
func TestUpdateDraftKeepsHTMLOnlyEditionAfterTemplateAdded(t *testing.T) {
	pinned := weeklyPublication()
	svc, repo := publicationSvc(pinned)
	n := repo.addDraft("p1", []string{"c1"})
	repo.mu.Lock()
	repo.drafts[n.ID].PublicationUID = &pinned.ID
	repo.drafts[n.ID].BodyLayout = nil
	repo.mu.Unlock()

	_, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID: n.ID, ExpectedVersion: n.Version, Subject: "s", BodyHTML: "<p>x</p>", BodyLayoutSet: true, BodyLayout: nil,
		EDReplyEmail: "e@example.com", CommitteeUIDs: []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
}

// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// ---- fakes ----------------------------------------------------------------

type fakeCommitteeClient struct {
	members map[string][]model.CommitteeMember
}

func (f *fakeCommitteeClient) ListMembers(_ context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	return f.members[committeeUID], nil
}

type fakeProjectClient struct{}

func (f *fakeProjectClient) Name(_ context.Context, _ string) (string, error) {
	return "Test Project", nil
}
func (f *fakeProjectClient) Slug(_ context.Context, _ string) (string, error) {
	return "test-project", nil
}

type capturedSend struct {
	To              string
	HTML            string
	Text            string
	From            string
	FromDisplayName string
	ReplyTo         string
}

type fakeEmailDispatcher struct {
	mu    sync.Mutex
	sends []capturedSend
}

func (f *fakeEmailDispatcher) SendEmail(_ context.Context, in port.SendEmailInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, capturedSend{
		To:              in.To,
		HTML:            in.HTML,
		Text:            in.Text,
		From:            in.From,
		FromDisplayName: in.FromDisplayName,
		ReplyTo:         in.ReplyTo,
	})
	return uuid.NewString(), nil
}
func (f *fakeEmailDispatcher) GetEngagement(_ context.Context, _ string) (*port.EmailEngagement, error) {
	return &port.EmailEngagement{}, nil
}
func (f *fakeEmailDispatcher) GetStatusByEmailID(_ context.Context, _ string) (*port.EmailRecipientRecord, error) {
	return &port.EmailRecipientRecord{}, nil
}
func (f *fakeEmailDispatcher) GetStatusByGroupID(_ context.Context, _ string) ([]port.EmailRecipientRecord, error) {
	return nil, nil
}
func (f *fakeEmailDispatcher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = nil
}
func (f *fakeEmailDispatcher) recipients() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sends))
	for _, s := range f.sends {
		out = append(out, s.To)
	}
	return out
}

// fakeNewsletterRepo holds drafts in memory and doubles as the
// UnsubscribeRepository so the E2E test can drive the full
// send → unsubscribe → resend flow without a database.
type fakeNewsletterRepo struct {
	mu      sync.Mutex
	drafts  map[uuid.UUID]*model.Newsletter
	unsubs  map[string]map[string]struct{}
	created []model.NewsletterUnsubscribe
}

func newFakeRepo() *fakeNewsletterRepo {
	return &fakeNewsletterRepo{
		drafts: make(map[uuid.UUID]*model.Newsletter),
		unsubs: make(map[string]map[string]struct{}),
	}
}

func (r *fakeNewsletterRepo) addDraft(projectUID string, committeeUIDs []string) *model.Newsletter {
	n := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    projectUID,
		Subject:       "Hello",
		BodyHTML:      "<p>Body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: committeeUIDs,
		Status:        model.StatusDraft,
		Version:       1,
	}
	r.drafts[n.ID] = n
	return n
}

func (r *fakeNewsletterRepo) Create(_ context.Context, n *model.Newsletter) error {
	r.drafts[n.ID] = n
	return nil
}
func (r *fakeNewsletterRepo) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return r.drafts[id], nil
}
func (r *fakeNewsletterRepo) List(_ context.Context, _ string) ([]*model.Newsletter, error) {
	return nil, nil
}
func (r *fakeNewsletterRepo) ListAll(_ context.Context, _ port.ListFilters) (*port.ListPage, error) {
	return &port.ListPage{}, nil
}
func (r *fakeNewsletterRepo) Update(_ context.Context, n *model.Newsletter, _ int64) (*model.Newsletter, error) {
	return n, nil
}
func (r *fakeNewsletterRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (r *fakeNewsletterRepo) MarkSent(_ context.Context, id uuid.UUID, sentAt time.Time, total int, groupID string, _ int64) (*model.Newsletter, error) {
	n := r.drafts[id]
	n.Status = model.StatusSent
	n.SentAt = &sentAt
	n.TotalRecipients = total
	n.GroupID = &groupID
	return n, nil
}
func (r *fakeNewsletterRepo) RecordOpen(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (r *fakeNewsletterRepo) Analytics(_ context.Context, _ uuid.UUID) (*model.Analytics, error) {
	return &model.Analytics{}, nil
}

func (r *fakeNewsletterRepo) CreateUnsubscribe(_ context.Context, projectUID, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unsubs[projectUID] == nil {
		r.unsubs[projectUID] = make(map[string]struct{})
	}
	r.unsubs[projectUID][strings.ToLower(email)] = struct{}{}
	r.created = append(r.created, model.NewsletterUnsubscribe{ProjectUID: projectUID, Email: strings.ToLower(email)})
	return nil
}
func (r *fakeNewsletterRepo) ListUnsubscribedEmails(_ context.Context, projectUID string) (map[string]struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]struct{})
	for e := range r.unsubs[projectUID] {
		out[e] = struct{}{}
	}
	return out, nil
}

// ---- helpers --------------------------------------------------------------

func newTestOrchestrator(repo *fakeNewsletterRepo, committee *fakeCommitteeClient, email *fakeEmailDispatcher, unsub *UnsubscribeService) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Unsubscribe:   unsub,
		Concurrency:   2,
		FanoutEnabled: true,
	})
}

// ---- tests ----------------------------------------------------------------

func TestResolveRecipientsExcludesUnsubscribed(t *testing.T) {
	repo := newFakeRepo()
	repo.unsubs["p1"] = map[string]struct{}{"alice@example.com": {}}
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {
			{Email: "Alice@Example.com", FirstName: "Alice"},
			{Email: "bob@example.com", FirstName: "Bob"},
		},
	}}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, &fakeEmailDispatcher{}, unsub)

	got, err := orch.Recipients(context.Background(), "p1", []string{"c1"})
	if err != nil {
		t.Fatalf("Recipients: %v", err)
	}
	if len(got) != 1 || got[0].Email != "bob@example.com" {
		t.Fatalf("got %v, want [bob@example.com]", got)
	}

	// Same committee under a different project must NOT exclude alice.
	got, err = orch.Recipients(context.Background(), "p2", []string{"c1"})
	if err != nil {
		t.Fatalf("Recipients p2: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("p2 got %d recipients, want 2 (unsubscribe is project-scoped)", len(got))
	}
}

func TestFanOutInjectsPerRecipientUnsubscribeURL(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {
			{Email: "alice@example.com"},
			{Email: "bob@example.com"},
		},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	draft := repo.addDraft("p1", []string{"c1"})
	_, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
	})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if len(email.sends) != 2 {
		t.Fatalf("got %d sends, want 2", len(email.sends))
	}
	for _, s := range email.sends {
		if strings.Contains(s.HTML, UnsubscribeURLPlaceholder) {
			t.Errorf("placeholder leaked into HTML for %s", s.To)
		}
		if !strings.Contains(s.HTML, "https://api.example/newsletters/unsubscribe?t=") {
			t.Errorf("HTML for %s missing unsubscribe link: %s", s.To, s.HTML)
		}
		// Token must decode to this recipient's email.
		_, after, _ := strings.Cut(s.HTML, "/newsletters/unsubscribe?t=")
		token, _, _ := strings.Cut(after, `"`)
		_, gotEmail, vErr := unsub.VerifyToken(token)
		if vErr != nil {
			t.Errorf("verify token for %s: %v", s.To, vErr)
		}
		if gotEmail != s.To {
			t.Errorf("token for %s decoded to %s", s.To, gotEmail)
		}
	}
}

// TestSendUnsubscribeResendExcludes is the end-to-end scenario the user
// asked for: send → click unsubscribe link → resend → confirm exclusion,
// and confirm the exclusion is project-scoped.
func TestSendUnsubscribeResendExcludes(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {
			{Email: "alice@example.com"},
			{Email: "bob@example.com"},
		},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("test-secret"), "http://localhost:8080")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	// --- 1. First send: both alice and bob receive the newsletter.
	first := repo.addDraft("p1", []string{"c1"})
	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: first.ID})
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if res.TotalRecipients != 2 || len(email.sends) != 2 {
		t.Fatalf("first send: got %d recipients / %d dispatches, want 2/2", res.TotalRecipients, len(email.sends))
	}

	// --- 2. Extract alice's unsubscribe URL from the HTML she was sent.
	var aliceHTML string
	for _, s := range email.sends {
		if s.To == "alice@example.com" {
			aliceHTML = s.HTML
		}
	}
	_, after, ok := strings.Cut(aliceHTML, "/newsletters/unsubscribe?t=")
	if !ok {
		t.Fatalf("alice's HTML missing unsubscribe link")
	}
	token, _, _ := strings.Cut(after, `"`)

	// --- 3. Alice "clicks" the link.
	gotProject, gotEmail, err := unsub.Unsubscribe(ctx, token)
	if err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if gotProject != "p1" || gotEmail != "alice@example.com" {
		t.Fatalf("unsubscribe decoded to (%s, %s), want (p1, alice@example.com)", gotProject, gotEmail)
	}
	if _, ok := repo.unsubs["p1"]["alice@example.com"]; !ok {
		t.Fatalf("alice not recorded in unsubscribe store")
	}

	// --- 4. Recipient count for p1 now reflects the exclusion.
	count, err := orch.RecipientCount(ctx, "p1", []string{"c1"})
	if err != nil {
		t.Fatalf("recipient count: %v", err)
	}
	if count != 1 {
		t.Fatalf("recipient count after unsubscribe = %d, want 1", count)
	}

	// --- 5. Second send for p1: only bob receives it.
	email.reset()
	second := repo.addDraft("p1", []string{"c1"})
	res, err = orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: second.ID})
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	got := email.recipients()
	if res.TotalRecipients != 1 || len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("second send: got recipients %v (total=%d), want [bob@example.com]", got, res.TotalRecipients)
	}

	// --- 6. Project-scoped: a send for p2 still includes alice.
	email.reset()
	other := repo.addDraft("p2", []string{"c1"})
	_, err = orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p2", NewsletterID: other.ID})
	if err != nil {
		t.Fatalf("p2 send: %v", err)
	}
	got = email.recipients()
	hasAlice := false
	for _, r := range got {
		if r == "alice@example.com" {
			hasAlice = true
		}
	}
	if !hasAlice {
		t.Fatalf("p2 send excluded alice; unsubscribe must be project-scoped. got %v", got)
	}
}

// TestSendNewsletterPopulatesEnvelope asserts the project-personalized From,
// From-Display-Name, and Reply-To are forwarded on every per-recipient send.
func TestSendNewsletterPopulatesEnvelope(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if len(email.sends) != 2 {
		t.Fatalf("got %d sends, want 2", len(email.sends))
	}
	for _, s := range email.sends {
		if s.From != defaultFromAddress {
			t.Errorf("send to %s: From=%q, want %q", s.To, s.From, defaultFromAddress)
		}
		if s.FromDisplayName != "Test Project Newsletter" {
			t.Errorf("send to %s: FromDisplayName=%q, want %q", s.To, s.FromDisplayName, "Test Project Newsletter")
		}
		if s.ReplyTo != draft.EDReplyEmail {
			t.Errorf("send to %s: ReplyTo=%q, want %q", s.To, s.ReplyTo, draft.EDReplyEmail)
		}
	}
}

// TestSendNewsletterUsesSenderDisplayName asserts that when the caller supplies
// the human display name of the user triggering the send, that name lands in
// the From display name on every per-recipient dispatch.
func TestSendNewsletterUsesSenderDisplayName(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{
		ProjectUID:        "p1",
		NewsletterID:      draft.ID,
		SenderDisplayName: "Jane Doe",
	}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if len(email.sends) != 2 {
		t.Fatalf("got %d sends, want 2", len(email.sends))
	}
	for _, s := range email.sends {
		if s.FromDisplayName != "Jane Doe" {
			t.Errorf("send to %s: FromDisplayName=%q, want %q", s.To, s.FromDisplayName, "Jane Doe")
		}
	}
}

// TestSendNewsletterSanitizesSenderDisplayName asserts CR/LF and other control
// characters are stripped from the SenderDisplayName (header-injection guard),
// and that a value which sanitizes to empty falls back to "<project> Newsletter".
func TestSendNewsletterSanitizesSenderDisplayName(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		sender  string
		wantFDN string
	}{
		{name: "strips CR LF", sender: "Jane\r\nBcc: evil@example.com", wantFDN: "JaneBcc: evil@example.com"},
		{name: "trims whitespace", sender: "   Jane Doe   ", wantFDN: "Jane Doe"},
		{name: "all control chars falls back", sender: "\r\n\t", wantFDN: "Test Project Newsletter"},
		{name: "empty falls back", sender: "", wantFDN: "Test Project Newsletter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
				"c1": {{Email: "alice@example.com"}},
			}}
			email := &fakeEmailDispatcher{}
			unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
			orch := newTestOrchestrator(repo, committee, email, unsub)

			draft := repo.addDraft("p1", []string{"c1"})
			if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{
				ProjectUID:        "p1",
				NewsletterID:      draft.ID,
				SenderDisplayName: tc.sender,
			}); err != nil {
				t.Fatalf("SendNewsletter: %v", err)
			}
			if len(email.sends) != 1 {
				t.Fatalf("got %d sends, want 1", len(email.sends))
			}
			if got := email.sends[0].FromDisplayName; got != tc.wantFDN {
				t.Errorf("FromDisplayName=%q, want %q", got, tc.wantFDN)
			}
		})
	}
}

// TestTestSendUsesSenderDisplayName asserts the test-send path honours
// SenderDisplayName the same way as the production send path.
func TestTestSendUsesSenderDisplayName(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	if err := orch.TestSend(ctx, TestSendInput{
		ProjectUID:        "p1",
		Subject:           "Hello",
		BodyHTML:          "<p>Body</p>",
		ToEmail:           "tester@example.com",
		SenderDisplayName: "Jane Doe",
	}); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	if got := email.sends[0].FromDisplayName; got != "Jane Doe" {
		t.Errorf("FromDisplayName=%q, want %q", got, "Jane Doe")
	}
}

// TestSendNewsletterRespectsFromAddressOverride asserts an explicit FromAddress
// on SendOrchestratorConfig wins over the built-in default.
func TestSendNewsletterRespectsFromAddressOverride(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Unsubscribe:   unsub,
		Concurrency:   1,
		FanoutEnabled: true,
		FromAddress:   "dev-newsletter@linuxfoundation.org",
	})

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	if got := email.sends[0].From; got != "dev-newsletter@linuxfoundation.org" {
		t.Errorf("From=%q, want %q", got, "dev-newsletter@linuxfoundation.org")
	}
}

// TestTestSendPopulatesEnvelope asserts the same project-personalized envelope
// fields land on the test-send path, with ReplyTo passed through from the
// request body and omitted when the body leaves it blank.
func TestTestSendPopulatesEnvelope(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		edReplyEmail string
		wantReplyTo  string
	}{
		{name: "with reply email", edReplyEmail: "ed@example.com", wantReplyTo: "ed@example.com"},
		{name: "without reply email", edReplyEmail: "", wantReplyTo: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			committee := &fakeCommitteeClient{}
			email := &fakeEmailDispatcher{}
			unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
			orch := newTestOrchestrator(repo, committee, email, unsub)

			err := orch.TestSend(ctx, TestSendInput{
				ProjectUID:   "p1",
				Subject:      "Hello",
				BodyHTML:     "<p>Body</p>",
				ToEmail:      "tester@example.com",
				EDReplyEmail: tc.edReplyEmail,
			})
			if err != nil {
				t.Fatalf("TestSend: %v", err)
			}
			if len(email.sends) != 1 {
				t.Fatalf("got %d sends, want 1", len(email.sends))
			}
			s := email.sends[0]
			if s.From != defaultFromAddress {
				t.Errorf("From=%q, want %q", s.From, defaultFromAddress)
			}
			if s.FromDisplayName != "Test Project Newsletter" {
				t.Errorf("FromDisplayName=%q, want %q", s.FromDisplayName, "Test Project Newsletter")
			}
			if s.ReplyTo != tc.wantReplyTo {
				t.Errorf("ReplyTo=%q, want %q", s.ReplyTo, tc.wantReplyTo)
			}
		})
	}
}

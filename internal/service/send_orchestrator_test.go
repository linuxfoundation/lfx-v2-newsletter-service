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
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// ---- fakes ----------------------------------------------------------------

type fakeCommitteeClient struct {
	members map[string][]model.CommitteeMember
}

func (f *fakeCommitteeClient) ListMembers(_ context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	return f.members[committeeUID], nil
}

// fakeProjectClient returns "Test Project" / "test-project" by default. Tests
// exercising the per-project From override set slug/slugErr to drive
// resolveFromAddress deterministically.
type fakeProjectClient struct {
	slug    string
	slugErr error
	name    string
}

func (f *fakeProjectClient) Name(_ context.Context, _ string) (string, error) {
	if f.name != "" {
		return f.name, nil
	}
	return "Test Project", nil
}
func (f *fakeProjectClient) Slug(_ context.Context, _ string) (string, error) {
	if f.slugErr != nil {
		return "", f.slugErr
	}
	if f.slug != "" {
		return f.slug, nil
	}
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
	cp := *n
	return &cp
}

// get returns a snapshot of the stored newsletter for test assertions.
func (r *fakeNewsletterRepo) get(id uuid.UUID) model.Newsletter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.drafts[id]
}

func (r *fakeNewsletterRepo) Create(_ context.Context, n *model.Newsletter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drafts[n.ID] = n
	return nil
}
func (r *fakeNewsletterRepo) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.drafts[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *n
	return &cp, nil
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
func (r *fakeNewsletterRepo) MarkSending(_ context.Context, id uuid.UUID, groupID string, total int, expectedVersion int64) (*model.Newsletter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.drafts[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if n.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}
	if n.Status == model.StatusSending {
		return nil, domain.ErrSendInProgress
	}
	if n.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}
	n.Status = model.StatusSending
	g := groupID
	n.GroupID = &g
	n.TotalRecipients = total
	n.Version++
	cp := *n
	return &cp, nil
}
func (r *fakeNewsletterRepo) MarkSent(_ context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.drafts[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if n.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}
	if n.Status != model.StatusSending || n.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}
	n.Status = model.StatusSent
	n.SentAt = &sentAt
	n.Version++
	cp := *n
	return &cp, nil
}
func (r *fakeNewsletterRepo) RevertSending(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.drafts[id]
	if !ok || n.Status != model.StatusSending {
		return domain.ErrNotFound
	}
	n.Status = model.StatusDraft
	n.GroupID = nil
	n.TotalRecipients = 0
	n.Version++
	return nil
}
func (r *fakeNewsletterRepo) RecoverStuckSending(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
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

func (r *fakeNewsletterRepo) ListUnsubscribes(_ context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.NewsletterUnsubscribe
	for _, u := range r.created {
		if u.ProjectUID == projectUID {
			out = append(out, &u)
		}
	}
	return out, nil
}

// fakeUserMetadataReader stands in for the auth-service NATS client. The
// orchestrator only calls it when a Principal is provided, so existing tests
// that leave Principal empty exercise the dev-mode fallback path without ever
// touching this fake.
type fakeUserMetadataReader struct {
	name string
	err  error
}

func (f *fakeUserMetadataReader) Name(_ context.Context, _ string) (string, error) {
	return f.name, f.err
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

// newTestOrchestratorWithUser wires a fake UserMetadataReader so tests can
// drive the auth-service NATS lookup deterministically.
func newTestOrchestratorWithUser(repo *fakeNewsletterRepo, committee *fakeCommitteeClient, email *fakeEmailDispatcher, unsub *UnsubscribeService, user port.UserMetadataReader) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		UserMetadata:  user,
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
	orch.Drain(context.Background())
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
	orch.Drain(ctx)
	if res.TotalRecipients != 2 || len(email.sends) != 2 {
		t.Fatalf("first send: got %d recipients / %d dispatches, want 2/2", res.TotalRecipients, len(email.sends))
	}
	if got := repo.get(first.ID); got.Status != model.StatusSent {
		t.Fatalf("first send settled to status %q, want %q", got.Status, model.StatusSent)
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
	orch.Drain(ctx)
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
	orch.Drain(ctx)
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
	orch.Drain(ctx)
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

// TestSendNewsletterUsesAuthServiceName asserts that when a Principal is
// supplied, the orchestrator resolves the sender's display name via the
// UserMetadataReader and stamps it on every per-recipient From line.
func TestSendNewsletterUsesAuthServiceName(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	users := &fakeUserMetadataReader{name: "Jane Doe"}
	orch := newTestOrchestratorWithUser(repo, committee, email, unsub, users)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Principal:    "auth0|abc",
	}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	orch.Drain(ctx)
	if len(email.sends) != 2 {
		t.Fatalf("got %d sends, want 2", len(email.sends))
	}
	for _, s := range email.sends {
		if s.FromDisplayName != "Jane Doe" {
			t.Errorf("send to %s: FromDisplayName=%q, want %q", s.To, s.FromDisplayName, "Jane Doe")
		}
	}
}

// TestSendNewsletterValidatesAuthServiceName asserts the orchestrator delegates
// safety of the resolved sender name to net/mail.ParseAddress: well-formed
// names are accepted (whitespace trimmed), an all-whitespace value falls back
// to the project-name chrome, and a name that won't parse as an RFC 5322
// display-name phrase (raw CR/LF, unbalanced quotes) blocks the send rather
// than being silently sanitised into something attacker-controlled.
func TestSendNewsletterValidatesAuthServiceName(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		nameVal  string
		wantFDN  string // checked when wantErr is false
		wantErr  bool
		wantSent int
	}{
		{name: "accepts simple name", nameVal: "Jane Doe", wantFDN: "Jane Doe", wantSent: 1},
		{name: "trims whitespace", nameVal: "   Jane Doe   ", wantFDN: "Jane Doe", wantSent: 1},
		{name: "all whitespace falls back to project", nameVal: "   \t  ", wantFDN: "Test Project Newsletter", wantSent: 1},
		{name: "rejects CR LF header injection", nameVal: "Jane\r\nBcc: evil@example.com", wantErr: true},
		{name: "rejects embedded NUL", nameVal: "Jane\x00Doe", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
				"c1": {{Email: "alice@example.com"}},
			}}
			email := &fakeEmailDispatcher{}
			unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
			users := &fakeUserMetadataReader{name: tc.nameVal}
			orch := newTestOrchestratorWithUser(repo, committee, email, unsub, users)

			draft := repo.addDraft("p1", []string{"c1"})
			_, err := orch.SendNewsletter(ctx, SendNewsletterInput{
				ProjectUID:   "p1",
				NewsletterID: draft.ID,
				Principal:    "auth0|abc",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if len(email.sends) != 0 {
					t.Fatalf("expected 0 dispatches on rejection, got %d", len(email.sends))
				}
				return
			}
			if err != nil {
				t.Fatalf("SendNewsletter: %v", err)
			}
			orch.Drain(ctx)
			if len(email.sends) != tc.wantSent {
				t.Fatalf("got %d sends, want %d", len(email.sends), tc.wantSent)
			}
			if got := email.sends[0].FromDisplayName; got != tc.wantFDN {
				t.Errorf("FromDisplayName=%q, want %q", got, tc.wantFDN)
			}
		})
	}
}

// TestSendNewsletterBlocksWhenAuthServiceFails asserts that a non-empty
// Principal whose auth-service lookup errors causes the send to bubble the
// error rather than dispatch with a fallback envelope. The error must
// classify as Unexpected (→ 5xx) and must NOT surface the upstream client's
// typed NotFound/Validation — the API consumer asked to send a newsletter,
// not to look up a user.
func TestSendNewsletterBlocksWhenAuthServiceFails(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		upstream  error
		assertion func(t *testing.T, err error)
	}{
		{
			name:     "transport error wraps as Unexpected",
			upstream: errors.New("auth-service unreachable"),
		},
		{
			name:     "upstream NotFound does not leak as NotFound",
			upstream: pkgerrors.NewNotFound("user not found"),
		},
		{
			name:     "upstream Validation does not leak as Validation",
			upstream: pkgerrors.NewValidation("principal malformed"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
				"c1": {{Email: "alice@example.com"}},
			}}
			email := &fakeEmailDispatcher{}
			unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
			users := &fakeUserMetadataReader{err: tc.upstream}
			orch := newTestOrchestratorWithUser(repo, committee, email, unsub, users)

			draft := repo.addDraft("p1", []string{"c1"})
			_, err := orch.SendNewsletter(ctx, SendNewsletterInput{
				ProjectUID:   "p1",
				NewsletterID: draft.ID,
				Principal:    "auth0|abc",
			})
			if err == nil {
				t.Fatal("expected error when auth-service lookup fails, got nil")
			}
			var unexpected pkgerrors.Unexpected
			if !errors.As(err, &unexpected) {
				t.Errorf("expected error to classify as Unexpected (→ 5xx), got %T: %v", err, err)
			}
			var notFound pkgerrors.NotFound
			if errors.As(err, &notFound) {
				t.Errorf("upstream NotFound leaked through to caller (would surface as 404): %v", err)
			}
			var validation pkgerrors.Validation
			if errors.As(err, &validation) {
				t.Errorf("upstream Validation leaked through to caller (would surface as 400): %v", err)
			}
			if len(email.sends) != 0 {
				t.Fatalf("expected 0 dispatches when send is blocked, got %d", len(email.sends))
			}
		})
	}
}

// TestSendNewsletterBlocksWhenUserMetadataMisconfigured asserts that a non-empty
// Principal with no UserMetadata client wired (production misconfiguration —
// auth is enabled but the dependency was never constructed) refuses the send
// rather than silently emitting an unattributed envelope.
func TestSendNewsletterBlocksWhenUserMetadataMisconfigured(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	// No UserMetadata wired — simulates prod misconfig with auth turned on.
	orch := newTestOrchestratorWithUser(repo, committee, email, unsub, nil)

	draft := repo.addDraft("p1", []string{"c1"})
	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Principal:    "auth0|abc",
	})
	if err == nil {
		t.Fatal("expected error when UserMetadata is not configured, got nil")
	}
	var unexpected pkgerrors.Unexpected
	if !errors.As(err, &unexpected) {
		t.Errorf("expected error to classify as Unexpected (→ 5xx), got %T: %v", err, err)
	}
	if len(email.sends) != 0 {
		t.Fatalf("expected 0 dispatches when UserMetadata is unwired, got %d", len(email.sends))
	}
}

// TestSendNewsletterDevModeFallback asserts that when no Principal is provided
// (auth disabled / local dev), the orchestrator skips the auth-service lookup
// entirely and falls back to the project-name chrome.
func TestSendNewsletterDevModeFallback(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	// A fake that would error if called — proves the orchestrator never calls it.
	users := &fakeUserMetadataReader{err: errors.New("must not be called when principal is empty")}
	orch := newTestOrchestratorWithUser(repo, committee, email, unsub, users)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
	}); err != nil {
		t.Fatalf("SendNewsletter (dev mode): %v", err)
	}
	orch.Drain(ctx)
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	if got := email.sends[0].FromDisplayName; got != "Test Project Newsletter" {
		t.Errorf("FromDisplayName=%q, want %q", got, "Test Project Newsletter")
	}
}

// TestTestSendUsesAuthServiceName asserts the test-send path honours the
// auth-service-resolved name the same way as the production send path.
func TestTestSendUsesAuthServiceName(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	users := &fakeUserMetadataReader{name: "Jane Doe"}
	orch := newTestOrchestratorWithUser(repo, committee, email, unsub, users)

	if err := orch.TestSend(ctx, TestSendInput{
		ProjectUID: "p1",
		Subject:    "Hello",
		BodyHTML:   "<p>Body</p>",
		ToEmail:    "tester@example.com",
		Principal:  "auth0|abc",
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
	orch.Drain(ctx)
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

// orchestratorWithOverrides wires an orchestrator with a configurable project
// client and From-address override map, bypassing the shared test helpers
// (which hardcode the project client and omit overrides).
func orchestratorWithOverrides(repo *fakeNewsletterRepo, committee *fakeCommitteeClient, email *fakeEmailDispatcher, unsub *UnsubscribeService, project port.ProjectMetadataClient, overrides map[string]string) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:                 repo,
		Committee:            committee,
		Project:              project,
		Email:                email,
		Unsubscribe:          unsub,
		Concurrency:          2,
		FanoutEnabled:        true,
		FromAddressOverrides: overrides,
	})
}

func TestSendNewsletterFromAddressOverride(t *testing.T) {
	const overrideAddr = "newsletter@lfx.aaif.io"
	overrides := map[string]string{"aaif": overrideAddr}

	tests := []struct {
		name      string
		project   *fakeProjectClient
		overrides map[string]string
		wantFrom  string
	}{
		{
			name:      "slug matches override",
			project:   &fakeProjectClient{slug: "aaif"},
			overrides: overrides,
			wantFrom:  overrideAddr,
		},
		{
			name:      "slug matches override case-insensitively",
			project:   &fakeProjectClient{slug: "AAIF"},
			overrides: overrides,
			wantFrom:  overrideAddr,
		},
		{
			name:      "slug absent from overrides uses default",
			project:   &fakeProjectClient{slug: "kubernetes"},
			overrides: overrides,
			wantFrom:  defaultFromAddress,
		},
		{
			name:      "no overrides configured uses default",
			project:   &fakeProjectClient{slug: "aaif"},
			overrides: nil,
			wantFrom:  defaultFromAddress,
		},
		{
			name:      "slug lookup error falls back to default",
			project:   &fakeProjectClient{slugErr: errors.New("projects-service unavailable")},
			overrides: overrides,
			wantFrom:  defaultFromAddress,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
				"c1": {{Email: "alice@example.com"}},
			}}
			email := &fakeEmailDispatcher{}
			unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
			orch := orchestratorWithOverrides(repo, committee, email, unsub, tc.project, tc.overrides)

			draft := repo.addDraft("p1", []string{"c1"})
			if _, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
				ProjectUID:   "p1",
				NewsletterID: draft.ID,
			}); err != nil {
				t.Fatalf("SendNewsletter: %v", err)
			}
			orch.Drain(context.Background())
			if len(email.sends) != 1 {
				t.Fatalf("got %d sends, want 1", len(email.sends))
			}
			if got := email.sends[0].From; got != tc.wantFrom {
				t.Errorf("From=%q, want %q", got, tc.wantFrom)
			}
		})
	}
}

func TestTestSendFromAddressOverride(t *testing.T) {
	const overrideAddr = "newsletter@lfx.aaif.io"
	repo := newFakeRepo()
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := orchestratorWithOverrides(repo, &fakeCommitteeClient{}, email, unsub,
		&fakeProjectClient{slug: "aaif"}, map[string]string{"aaif": overrideAddr})

	if err := orch.TestSend(context.Background(), TestSendInput{
		ProjectUID: "p1",
		Subject:    "Hello",
		BodyHTML:   "<p>Body</p>",
		ToEmail:    "tester@example.com",
	}); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	if got := email.sends[0].From; got != overrideAddr {
		t.Errorf("From=%q, want %q", got, overrideAddr)
	}
}

// ---- async send behavior ----------------------------------------------------

// gatedEmailDispatcher blocks every SendEmail until release is closed, letting
// tests observe the accepted-but-still-sending state deterministically.
type gatedEmailDispatcher struct {
	fakeEmailDispatcher
	release chan struct{}
	started chan struct{}
}

func (g *gatedEmailDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	select {
	case g.started <- struct{}{}:
	default:
	}
	<-g.release
	return g.fakeEmailDispatcher.SendEmail(ctx, in)
}

// failingEmailDispatcher rejects every SendEmail, driving the total-failure path.
type failingEmailDispatcher struct {
	fakeEmailDispatcher
}

func (f *failingEmailDispatcher) SendEmail(_ context.Context, _ port.SendEmailInput) (string, error) {
	return "", errors.New("email-service unreachable")
}

func newGatedOrchestrator(repo *fakeNewsletterRepo, committee *fakeCommitteeClient, email port.EmailDispatcher) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Unsubscribe:   NewUnsubscribeService(repo, []byte("k"), "https://api.example"),
		Concurrency:   2,
		FanoutEnabled: true,
	})
}

// TestSendNewsletterAcceptsBeforeFanOutCompletes asserts the send returns the
// sending-state newsletter while the fan-out is still in flight, and that the
// row settles to sent once the fan-out completes.
func TestSendNewsletterAcceptsBeforeFanOutCompletes(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	gate := &gatedEmailDispatcher{release: make(chan struct{}), started: make(chan struct{}, 1)}
	orch := newGatedOrchestrator(repo, committee, gate)

	draft := repo.addDraft("p1", []string{"c1"})
	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if res.Newsletter.Status != model.StatusSending {
		t.Fatalf("accepted send returned status %q, want %q", res.Newsletter.Status, model.StatusSending)
	}
	if res.Sent != 0 || res.Failed != 0 || len(res.Failures) != 0 {
		t.Fatalf("accepted send reported sent=%d failed=%d failures=%d, want all zero", res.Sent, res.Failed, len(res.Failures))
	}
	if res.TotalRecipients != 2 {
		t.Fatalf("TotalRecipients=%d, want 2", res.TotalRecipients)
	}
	if got := repo.get(draft.ID); got.Status != model.StatusSending {
		t.Fatalf("row status %q while fan-out in flight, want %q", got.Status, model.StatusSending)
	}
	if got := repo.get(draft.ID); got.GroupID == nil || *got.GroupID != res.GroupID {
		t.Fatalf("group_id not persisted at the sending transition")
	}

	close(gate.release)
	orch.Drain(ctx)

	settled := repo.get(draft.ID)
	if settled.Status != model.StatusSent {
		t.Fatalf("row settled to %q, want %q", settled.Status, model.StatusSent)
	}
	if settled.SentAt == nil {
		t.Fatalf("sent_at not set on settle")
	}
	if len(gate.recipients()) != 2 {
		t.Fatalf("dispatched %d, want 2", len(gate.recipients()))
	}
}

// TestSendNewsletterDetachedFromCallerCancel asserts that cancelling the
// request context after acceptance neither cancels the in-flight fan-out nor
// orphans the status transition — the scenario behind the production bug where
// a BFF 30s abort stranded delivered newsletters in draft.
func TestSendNewsletterDetachedFromCallerCancel(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	gate := &gatedEmailDispatcher{release: make(chan struct{}), started: make(chan struct{}, 1)}
	orch := newGatedOrchestrator(repo, committee, gate)

	reqCtx, cancel := context.WithCancel(context.Background())
	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(reqCtx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}

	// Simulate the client (BFF) aborting while the fan-out is blocked mid-flight.
	<-gate.started
	cancel()
	close(gate.release)
	orch.Drain(context.Background())

	if got := gate.recipients(); len(got) != 2 {
		t.Fatalf("dispatched %d recipients after caller cancel, want 2 (fan-out must be detached)", len(got))
	}
	if got := repo.get(draft.ID); got.Status != model.StatusSent {
		t.Fatalf("row settled to %q after caller cancel, want %q", got.Status, model.StatusSent)
	}
}

// TestSendNewsletterRejectsConcurrentSend asserts the sending status is an
// effective duplicate-send guard: a second send request while the first
// fan-out is in flight is rejected with ErrSendInProgress and dispatches
// nothing extra.
func TestSendNewsletterRejectsConcurrentSend(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	gate := &gatedEmailDispatcher{release: make(chan struct{}), started: make(chan struct{}, 1)}
	orch := newGatedOrchestrator(repo, committee, gate)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("first send: %v", err)
	}

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	if !errors.Is(err, domain.ErrSendInProgress) {
		t.Fatalf("second send while in flight: got err=%v, want ErrSendInProgress", err)
	}

	close(gate.release)
	orch.Drain(ctx)
	if got := gate.recipients(); len(got) != 1 {
		t.Fatalf("dispatched %d total, want 1 (no duplicate fan-out)", len(got))
	}

	// After the send settles, a re-send is rejected as already sent.
	_, err = orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	if !errors.Is(err, domain.ErrAlreadySent) {
		t.Fatalf("re-send after settle: got err=%v, want ErrAlreadySent", err)
	}
}

// TestSendNewsletterTotalFailureRevertsToDraft asserts the safety gate: when
// zero recipients could be delivered to, the newsletter reverts to draft (with
// send-time fields cleared) so the operator can retry.
func TestSendNewsletterTotalFailureRevertsToDraft(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	orch := newGatedOrchestrator(repo, committee, &failingEmailDispatcher{})

	draft := repo.addDraft("p1", []string{"c1"})
	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if res.Newsletter.Status != model.StatusSending {
		t.Fatalf("accepted send returned status %q, want %q", res.Newsletter.Status, model.StatusSending)
	}

	orch.Drain(ctx)

	got := repo.get(draft.ID)
	if got.Status != model.StatusDraft {
		t.Fatalf("total failure settled to %q, want %q (retry path)", got.Status, model.StatusDraft)
	}
	if got.GroupID != nil || got.TotalRecipients != 0 || got.SentAt != nil {
		t.Fatalf("revert did not clear send-time fields: group_id=%v total=%d sent_at=%v", got.GroupID, got.TotalRecipients, got.SentAt)
	}
}

// TestSendNewsletterZeroRecipientsSettlesSynchronously asserts the historical
// zero-audience contract: no fan-out, marked sent before the response returns.
func TestSendNewsletterZeroRecipientsSettlesSynchronously(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{}}
	email := &fakeEmailDispatcher{}
	orch := newGatedOrchestrator(repo, committee, email)

	draft := repo.addDraft("p1", []string{"c1"})
	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if res.Newsletter.Status != model.StatusSent {
		t.Fatalf("zero-recipient send returned status %q, want %q", res.Newsletter.Status, model.StatusSent)
	}
	if res.TotalRecipients != 0 || len(email.sends) != 0 {
		t.Fatalf("zero-recipient send dispatched %d to %d recipients, want none", len(email.sends), res.TotalRecipients)
	}
}

// TestDraftGuardsWhileSending asserts edits and deletes are rejected while a
// send fan-out is in flight — the server-side backstop for the autosave race
// that stranded delivered newsletters in draft.
func TestDraftGuardsWhileSending(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := repo.MarkSending(ctx, draft.ID, uuid.NewString(), 1, draft.Version); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}

	_, err := svc.UpdateDraft(ctx, "p1", UpdateDraftInput{
		ID:              draft.ID,
		ExpectedVersion: draft.Version + 1,
		Subject:         "Edited",
		BodyHTML:        "<p>Edited</p>",
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if !errors.Is(err, domain.ErrSendInProgress) {
		t.Fatalf("UpdateDraft while sending: got err=%v, want ErrSendInProgress", err)
	}

	if err := svc.DeleteDraft(ctx, "p1", draft.ID); !errors.Is(err, domain.ErrSendInProgress) {
		t.Fatalf("DeleteDraft while sending: got err=%v, want ErrSendInProgress", err)
	}
}

// addLayoutDraft stores a layout-based draft: BodyLayout is non-empty (so the
// orchestrator takes the layout send path) and BodyHTML is a hand-built stand-in
// that embeds all three runtime placeholders so the test exercises placeholder
// substitution (including %%VIEW_ONLINE_URL%% collapsing to empty). It is NOT a
// byte-for-byte mirror of render-on-write output, which drops the View Online row
// and persists snake_case layout JSON.
func (r *fakeNewsletterRepo) addLayoutDraft(projectUID string, committeeUIDs []string) *model.Newsletter {
	n := &model.Newsletter{
		ID:         uuid.New(),
		ProjectUID: projectUID,
		Subject:    "Layout Hello",
		// A minimal stand-in for the emitter's full email: a <body>-level
		// document with the wrapper's runtime placeholders. It deliberately
		// contains none of the email_chrome envelope markers.
		BodyHTML: `<!DOCTYPE html><html><body>` +
			`<div id="emitter-content">Emitter body</div>` +
			`<a href="` + ViewOnlineURLPlaceholder + `">View Online</a>` +
			`<a href="` + UnsubscribeURLPlaceholder + `">Unsubscribe</a>` +
			`<a href="` + ManageSubscriptionsURLPlaceholder + `">Manage your email preferences</a>` +
			`<p>From ` + SenderNamePlaceholder + ` for ` + ProjectNamePlaceholder + `</p>` +
			`</body></html>`,
		BodyLayout:    json.RawMessage(`{"wrapper_key":"default","blocks":[{"block_type":"intro_paragraph"}]}`),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: committeeUIDs,
		Status:        model.StatusDraft,
		Version:       1,
	}
	r.drafts[n.ID] = n
	return n
}

// chromeEnvelopeMarkers are strings the email_chrome renderer always emits but
// that a raw emitter (layout) email never contains. Their absence proves the
// layout path did NOT re-wrap the body in chrome.
var chromeEnvelopeMarkers = []string{
	"&middot; Newsletter", // chrome header eyebrow
	`class="lfx-pad"`,     // chrome table cell class
}

// TestSendNewsletterLayoutNotDoubleWrapped asserts a layout-based newsletter is
// dispatched with body_html used directly (emitter content present, no chrome
// envelope) and with the three runtime placeholders substituted to real
// per-recipient values.
func TestSendNewsletterLayoutNotDoubleWrapped(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	draft := repo.addLayoutDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	orch.Drain(ctx)
	if len(email.sends) != 2 {
		t.Fatalf("got %d sends, want 2", len(email.sends))
	}
	for _, s := range email.sends {
		// Emitter content must be present (body_html used directly).
		if !strings.Contains(s.HTML, "Emitter body") {
			t.Errorf("send to %s: emitter content missing: %s", s.To, s.HTML)
		}
		// No chrome envelope markers — the layout path must not double-wrap.
		for _, marker := range chromeEnvelopeMarkers {
			if strings.Contains(s.HTML, marker) {
				t.Errorf("send to %s: chrome marker %q leaked into layout email (double-wrapped)", s.To, marker)
			}
		}
		// All three placeholders substituted: none of the raw sentinels remain.
		for _, ph := range []string{UnsubscribeURLPlaceholder, ManageSubscriptionsURLPlaceholder, ViewOnlineURLPlaceholder} {
			if strings.Contains(s.HTML, ph) {
				t.Errorf("send to %s: placeholder %q not substituted", s.To, ph)
			}
		}
		// Only the unsubscribe link resolves to the recipient's signed URL;
		// manage-preferences resolves empty (asserted below).
		wantURL := unsub.BuildURL("p1", s.To)
		if got := strings.Count(s.HTML, wantURL); got != 1 {
			t.Errorf("send to %s: expected the unsubscribe link only to use %q (1 occurrence), got %d in %s", s.To, wantURL, got, s.HTML)
		}
		// Manage-preferences must NOT alias the destructive one-click
		// unsubscribe URL: its sentinel resolves to empty.
		if strings.Contains(s.HTML, `<a href="`+wantURL+`">Manage`) {
			t.Errorf("send to %s: manage-preferences link aliases the unsubscribe URL", s.To)
		}
		// Send-scoped sentinels substituted with the resolved names.
		if !strings.Contains(s.HTML, "From Test Sender for") && !strings.Contains(s.HTML, "From ") {
			t.Errorf("send to %s: sender/project sentinels not substituted: %s", s.To, s.HTML)
		}
		for _, ph := range []string{SenderNamePlaceholder, ProjectNamePlaceholder} {
			if strings.Contains(s.HTML, ph) {
				t.Errorf("send to %s: send-scoped sentinel %q not substituted", s.To, ph)
			}
		}
		// View Online resolved to empty (no hosted web version yet) — the href
		// collapses to an empty string rather than leaving the sentinel.
		if !strings.Contains(s.HTML, `<a href="">View Online</a>`) {
			t.Errorf("send to %s: view-online placeholder not emptied: %s", s.To, s.HTML)
		}
		// Text body derived from the final HTML via the strip pass.
		if !strings.Contains(s.Text, "Emitter body") {
			t.Errorf("send to %s: text body missing emitter content: %q", s.To, s.Text)
		}
		// The substituted unsubscribe URL survives into text (link preserved).
		if !strings.Contains(s.Text, wantURL) {
			t.Errorf("send to %s: text body missing unsubscribe URL: %q", s.To, s.Text)
		}
	}
}

// TestSendNewsletterLegacyStillUsesChrome asserts the legacy (no-layout) path is
// unchanged: the body is wrapped in the email_chrome envelope, complete with
// the header eyebrow and compliance footer.
func TestSendNewsletterLegacyStillUsesChrome(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	draft := repo.addDraft("p1", []string{"c1"}) // legacy: BodyLayout nil
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	orch.Drain(ctx)
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	s := email.sends[0]
	// Every chrome envelope marker must be present on the legacy path.
	for _, marker := range chromeEnvelopeMarkers {
		if !strings.Contains(s.HTML, marker) {
			t.Errorf("legacy send missing chrome marker %q: %s", marker, s.HTML)
		}
	}
	// Authored body still rendered inside the envelope.
	if !strings.Contains(s.HTML, "Body") {
		t.Errorf("legacy send missing authored body: %s", s.HTML)
	}
	// Unsubscribe placeholder substituted to the recipient's link, and the
	// layout-only sentinels never appear in a legacy body.
	if strings.Contains(s.HTML, UnsubscribeURLPlaceholder) {
		t.Errorf("legacy send left unsubscribe placeholder unsubstituted: %s", s.HTML)
	}
	if !strings.Contains(s.HTML, unsub.BuildURL("p1", s.To)) {
		t.Errorf("legacy send missing per-recipient unsubscribe link: %s", s.HTML)
	}
}

// TestTestSendLayoutNotDoubleWrapped asserts the test-send path honours the
// same layout branch: IsLayout=true dispatches body_html directly (no chrome)
// with the placeholders bound for the single test recipient.
func TestTestSendLayoutNotDoubleWrapped(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	layoutHTML := `<!DOCTYPE html><html><body>` +
		`<div id="emitter-content">Emitter body</div>` +
		`<a href="` + UnsubscribeURLPlaceholder + `">Unsubscribe</a>` +
		`<a href="` + ManageSubscriptionsURLPlaceholder + `">Manage</a>` +
		`<a href="` + ViewOnlineURLPlaceholder + `">View Online</a>` +
		`</body></html>`

	if err := orch.TestSend(ctx, TestSendInput{
		ProjectUID: "p1",
		Subject:    "Hello",
		BodyHTML:   layoutHTML,
		ToEmail:    "tester@example.com",
		IsLayout:   true,
	}); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	s := email.sends[0]
	if !strings.Contains(s.HTML, "Emitter body") {
		t.Errorf("test-send layout missing emitter content: %s", s.HTML)
	}
	for _, marker := range chromeEnvelopeMarkers {
		if strings.Contains(s.HTML, marker) {
			t.Errorf("test-send layout double-wrapped: chrome marker %q present: %s", marker, s.HTML)
		}
	}
	wantURL := unsub.BuildURL("p1", "tester@example.com")
	if got := strings.Count(s.HTML, wantURL); got != 1 {
		t.Errorf("test-send: expected unsubscribe+manage to use %q (2x), got %d: %s", wantURL, got, s.HTML)
	}
	if strings.Contains(s.HTML, ViewOnlineURLPlaceholder) {
		t.Errorf("test-send: view-online placeholder not substituted: %s", s.HTML)
	}
}

// TestTestSendLegacyStillUsesChrome asserts the legacy test-send (IsLayout
// false / unset) is unchanged: chrome-wrapped, no compliance footer (test
// sends never carry it).
func TestTestSendLegacyStillUsesChrome(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)

	if err := orch.TestSend(ctx, TestSendInput{
		ProjectUID: "p1",
		Subject:    "Hello",
		BodyHTML:   "<p>Body</p>",
		ToEmail:    "tester@example.com",
	}); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	s := email.sends[0]
	// Chrome header eyebrow present; the compliance footer is intentionally
	// absent on the test-send path, so only assert the header marker.
	if !strings.Contains(s.HTML, "&middot; Newsletter") {
		t.Errorf("legacy test-send missing chrome header: %s", s.HTML)
	}
	if strings.Contains(s.HTML, "Sent by") {
		t.Errorf("test-send must not carry the compliance footer: %s", s.HTML)
	}
	if !strings.Contains(s.HTML, "Body") {
		t.Errorf("legacy test-send missing authored body: %s", s.HTML)
	}
}

// TestSubstitution_TextBodyNotHTMLEscaped pins the HTML-vs-text escaping
// split: send-scoped and per-recipient substitutions escape values for the
// HTML part but leave the plain-text part raw, so a name like "R&D" does not
// become "R&amp;D" in text/plain.
func TestSubstitution_TextBodyNotHTMLEscaped(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	orch := newTestOrchestrator(repo, committee, email, unsub)
	orch.project = &fakeProjectClient{name: "R&D <Team>"}

	draft := repo.addLayoutDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	orch.Drain(context.Background())
	if len(email.sends) != 1 {
		t.Fatalf("got %d sends, want 1", len(email.sends))
	}
	s := email.sends[0]
	// HTML part: escaped.
	if !strings.Contains(s.HTML, "R&amp;D") {
		t.Errorf("HTML body should HTML-escape the project name; got %s", s.HTML)
	}
	// Text part: raw, no entities.
	if strings.Contains(s.Text, "&amp;") || strings.Contains(s.Text, "&lt;") {
		t.Errorf("text body must not contain HTML entities; got %s", s.Text)
	}
	if !strings.Contains(s.Text, "R&D <Team>") {
		t.Errorf("text body should carry the raw project name; got %s", s.Text)
	}
}

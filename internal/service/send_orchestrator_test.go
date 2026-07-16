// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"fmt"
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
}

func (f *fakeProjectClient) Name(_ context.Context, _ string) (string, error) {
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
	svc := NewNewsletterService(repo)

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

// ---- dispatch retry ----------------------------------------------------------

// scriptedEmailDispatcher fails SendEmail with the queued errs in order (one
// per attempt); once they run out it fails with alwaysErr when set, otherwise
// it delegates to the embedded fake (success). It counts every attempt so
// retry tests can assert exactly how many dispatches were issued. onFail, when
// set, runs after each scripted failure — used to cancel the context between
// an attempt and its backoff wait.
type scriptedEmailDispatcher struct {
	fakeEmailDispatcher
	scriptMu  sync.Mutex
	errs      []error
	alwaysErr error
	attempts  int
	onFail    func(attempt int)
}

func (s *scriptedEmailDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	s.scriptMu.Lock()
	s.attempts++
	attempt := s.attempts
	err := s.alwaysErr
	if len(s.errs) > 0 {
		err = s.errs[0]
		s.errs = s.errs[1:]
	}
	hook := s.onFail
	s.scriptMu.Unlock()
	if err != nil {
		if hook != nil {
			hook(attempt)
		}
		return "", err
	}
	return s.fakeEmailDispatcher.SendEmail(ctx, in)
}

func (s *scriptedEmailDispatcher) attemptCount() int {
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	return s.attempts
}

// retryableDispatchErr builds a failure tagged retry-safe, mirroring the shape
// the NATS dispatcher produces for explicit error replies and no-responders.
func retryableDispatchErr(msg string) error {
	return fmt.Errorf("%s: %w", msg, domain.ErrEmailNotDispatched)
}

// newRetryOrchestrator wires an orchestrator with a millisecond retry backoff
// so retry-path tests stay fast.
func newRetryOrchestrator(email port.EmailDispatcher) *SendOrchestrator {
	o := NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          newFakeRepo(),
		Committee:     &fakeCommitteeClient{},
		Project:       &fakeProjectClient{},
		Email:         email,
		Concurrency:   2,
		FanoutEnabled: true,
	})
	o.retryBackoff = time.Millisecond
	return o
}

func testSendInput(to string) port.SendEmailInput {
	return port.SendEmailInput{To: to, Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"}
}

// TestDispatchWithRetrySucceedsAfterRetryableFailures asserts a recipient that
// fails retry-safely twice is delivered on the third (final) attempt.
func TestDispatchWithRetrySucceedsAfterRetryableFailures(t *testing.T) {
	email := &scriptedEmailDispatcher{errs: []error{
		retryableDispatchErr("first"),
		retryableDispatchErr("second"),
	}}
	orch := newRetryOrchestrator(email)

	exhausted, err := orch.dispatchWithRetry(context.Background(), testSendInput("alice@example.com"))
	if err != nil {
		t.Fatalf("dispatchWithRetry: %v, want success on final attempt", err)
	}
	if exhausted {
		t.Fatalf("exhausted=true on success, want false")
	}
	if got := email.attemptCount(); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
}

// TestDispatchWithRetryDoesNotRetryAmbiguousFailures asserts the retry-safety
// boundary: failures that do not prove the email was never dispatched (request
// timeout, cancelled context, generic transport errors) are terminal on the
// first attempt — email-service has no idempotency key, so retrying them could
// double-send.
func TestDispatchWithRetryDoesNotRetryAmbiguousFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"request timeout", pkgerrors.NewServiceUnavailable("NATS request failed", context.DeadlineExceeded)},
		{"cancelled context", pkgerrors.NewServiceUnavailable("NATS request failed", context.Canceled)},
		{"generic transport error", errors.New("email-service unreachable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			email := &scriptedEmailDispatcher{errs: []error{tc.err, tc.err, tc.err}}
			orch := newRetryOrchestrator(email)

			exhausted, err := orch.dispatchWithRetry(context.Background(), testSendInput("alice@example.com"))
			if !errors.Is(err, tc.err) {
				t.Fatalf("dispatchWithRetry err=%v, want the original dispatch error", err)
			}
			if exhausted {
				t.Fatalf("exhausted=true for a terminal-on-first-failure error, want false")
			}
			if got := email.attemptCount(); got != 1 {
				t.Fatalf("attempts=%d, want 1 (ambiguous failures must not be retried)", got)
			}
		})
	}
}

// TestDispatchWithRetryExhaustionReturnsLastError asserts a recipient failing
// retry-safely on every attempt is dispatched exactly dispatchMaxAttempts
// times and surfaces the final attempt's error.
func TestDispatchWithRetryExhaustionReturnsLastError(t *testing.T) {
	email := &scriptedEmailDispatcher{errs: []error{
		retryableDispatchErr("first"),
		retryableDispatchErr("second"),
		retryableDispatchErr("third"),
	}}
	orch := newRetryOrchestrator(email)

	exhausted, err := orch.dispatchWithRetry(context.Background(), testSendInput("alice@example.com"))
	if err == nil {
		t.Fatalf("dispatchWithRetry succeeded, want exhaustion")
	}
	if !exhausted {
		t.Fatalf("exhausted=false after spending every attempt, want true")
	}
	if got := email.attemptCount(); got != dispatchMaxAttempts {
		t.Fatalf("attempts=%d, want %d", got, dispatchMaxAttempts)
	}
	if !strings.Contains(err.Error(), "third") {
		t.Fatalf("err=%q, want the last attempt's error", err)
	}
}

// TestDispatchWithRetryCtxCancelledMidBackoff asserts a context cancelled
// during the backoff wait aborts the remaining retries promptly and surfaces
// the last dispatch error (not the winding-down context) to failure
// accounting.
func TestDispatchWithRetryCtxCancelledMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	email := &scriptedEmailDispatcher{
		errs:   []error{retryableDispatchErr("email-service rejected")},
		onFail: func(int) { cancel() },
	}
	orch := newRetryOrchestrator(email)
	orch.retryBackoff = 30 * time.Second // cancellation must win, not the timer

	start := time.Now()
	exhausted, err := orch.dispatchWithRetry(ctx, testSendInput("alice@example.com"))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("dispatchWithRetry blocked %v in backoff after cancel", elapsed)
	}
	if !errors.Is(err, domain.ErrEmailNotDispatched) || !strings.Contains(err.Error(), "email-service rejected") {
		t.Fatalf("err=%v, want the last dispatch error", err)
	}
	if exhausted {
		t.Fatalf("exhausted=true after ctx cancellation cut retries short, want false")
	}
	if got := email.attemptCount(); got != 1 {
		t.Fatalf("attempts=%d, want 1 (no retry after cancellation)", got)
	}
}

// perRecipientDispatcher fails every attempt for failAddr with a retry-safe
// error and succeeds for all other recipients, tracking attempts per address.
type perRecipientDispatcher struct {
	fakeEmailDispatcher
	failAddr string
	countMu  sync.Mutex
	perAddr  map[string]int
}

func (p *perRecipientDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	p.countMu.Lock()
	if p.perAddr == nil {
		p.perAddr = make(map[string]int)
	}
	p.perAddr[in.To]++
	p.countMu.Unlock()
	if in.To == p.failAddr {
		return "", retryableDispatchErr("email-service rejected")
	}
	return p.fakeEmailDispatcher.SendEmail(ctx, in)
}

func (p *perRecipientDispatcher) attemptsFor(addr string) int {
	p.countMu.Lock()
	defer p.countMu.Unlock()
	return p.perAddr[addr]
}

// TestFanOutRetryAccounting asserts failure accounting is unchanged by the
// retry loop: an exhausted recipient counts as failed exactly once with the
// last error, a first-attempt success is dispatched exactly once, and the
// concurrency-capped worker owns all attempts for its recipient.
func TestFanOutRetryAccounting(t *testing.T) {
	email := &perRecipientDispatcher{failAddr: "alice@example.com"}
	orch := newRetryOrchestrator(email)

	sent, failed, failures := orch.fanOut(context.Background(), "p1", []model.CommitteeMember{
		{Email: "alice@example.com"},
		{Email: "bob@example.com"},
	}, emailEnvelope{Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"})

	if sent != 1 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, want 1/1", sent, failed)
	}
	if len(failures) != 1 || failures[0].Email != "alice@example.com" {
		t.Fatalf("failures=%v, want exactly one entry for alice (failed recipients count once, after the final attempt)", failures)
	}
	if !strings.Contains(failures[0].Error, "email-service rejected") {
		t.Fatalf("failure error=%q, want the last dispatch error", failures[0].Error)
	}
	if got := email.attemptsFor("alice@example.com"); got != dispatchMaxAttempts {
		t.Fatalf("alice attempts=%d, want %d", got, dispatchMaxAttempts)
	}
	if got := email.attemptsFor("bob@example.com"); got != 1 {
		t.Fatalf("bob attempts=%d, want 1 (no retry on success)", got)
	}
}

// ---- systemic outage fail-fast ----------------------------------------------

// unreachableDispatchErr builds a failure carrying both sentinels the NATS
// dispatcher attaches to a no-responders condition: retry-safe, and
// authoritative proof email-service is not subscribed to the send subject.
// That the real dispatcher emits both is pinned by TestClassifySendRequestError
// in the nats package — this helper only mirrors the shape.
func unreachableDispatchErr(msg string) error {
	return fmt.Errorf("%s: %w: %w", msg, domain.ErrEmailNotDispatched, domain.ErrEmailServiceUnreachable)
}

// waitFor receives from ch with a deadline, so a behavioral regression surfaces
// as a named failure rather than a package-wide hang.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestFanOutOutageSkipsRecipientsNotYetDispatched asserts the fail-fast: the
// first recipient spends its full attempt budget proving email-service has no
// responders, and every recipient after it is skipped without a single
// dispatch attempt — the whole point being not to burn the job timeout
// delivering nothing.
func TestFanOutOutageSkipsRecipientsNotYetDispatched(t *testing.T) {
	email := &scriptedEmailDispatcher{alwaysErr: unreachableDispatchErr("no responders")}
	orch := newRetryOrchestrator(email)
	orch.concurrency = 1 // serialize so the trip provably precedes the rest

	sent, failed, failures := orch.fanOut(context.Background(), "p1", []model.CommitteeMember{
		{Email: "alice@example.com"},
		{Email: "bob@example.com"},
		{Email: "carol@example.com"},
	}, emailEnvelope{Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"})

	if sent != 0 || failed != 3 {
		t.Fatalf("sent=%d failed=%d, want 0/3", sent, failed)
	}
	// Only the recipient that tripped the outage may spend attempts.
	if got := email.attemptCount(); got != dispatchMaxAttempts {
		t.Fatalf("total attempts=%d, want %d (only the tripping recipient dispatches)", got, dispatchMaxAttempts)
	}
	if len(failures) != 3 {
		t.Fatalf("failures=%d, want 3", len(failures))
	}
	if failures[0].Email != "alice@example.com" || failures[0].Error == outageSkipReason {
		t.Fatalf("failures[0]=%+v, want the tripping recipient recorded with its dispatch error", failures[0])
	}
	for _, f := range failures[1:] {
		if f.Error != outageSkipReason {
			t.Fatalf("failure for %s recorded %q, want %q", f.Email, f.Error, outageSkipReason)
		}
	}
}

// TestFanOutOutageDoesNotTripOnRecoveredBlip asserts a no-responders blip that
// the retry rides out leaves the rest of the list dispatching normally — the
// outage must follow from a subject that stays dead, not from one failure.
//
// Scope, honestly stated: a ridden-out blip ends in err == nil, so it is the
// error check alone that rejects the trip here — this test does not exercise
// the `exhausted` guard. That guard is pinned by
// TestFanOutOutageRequiresExhaustionNotMerelyUnreachable at this level, and by
// TestDispatchWithRetryCtxCancelledOnUnreachableReportsNotExhausted one level
// down.
func TestFanOutOutageDoesNotTripOnRecoveredBlip(t *testing.T) {
	email := &scriptedEmailDispatcher{errs: []error{unreachableDispatchErr("momentary blip")}}
	orch := newRetryOrchestrator(email)
	orch.concurrency = 1

	sent, failed, failures := orch.fanOut(context.Background(), "p1", []model.CommitteeMember{
		{Email: "alice@example.com"},
		{Email: "bob@example.com"},
	}, emailEnvelope{Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"})

	if sent != 2 || failed != 0 {
		t.Fatalf("sent=%d failed=%d failures=%v, want 2/0 (a ridden-out blip must not trip the outage)", sent, failed, failures)
	}
	// alice: 1 failed attempt + 1 success; bob: 1 success.
	if got := email.attemptCount(); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
}

// outageMixDispatcher parks the "inflight" recipient inside SendEmail until
// released, while every other recipient fails with no-responders. It lets a
// test hold one dispatch in flight across the moment the outage trips.
type outageMixDispatcher struct {
	fakeEmailDispatcher
	inflightAddr string
	release      chan struct{}
	started      chan struct{}
	countMu      sync.Mutex
	perAddr      map[string]int
}

func (d *outageMixDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	d.countMu.Lock()
	if d.perAddr == nil {
		d.perAddr = make(map[string]int)
	}
	d.perAddr[in.To]++
	d.countMu.Unlock()
	if in.To == d.inflightAddr {
		select {
		case d.started <- struct{}{}:
		default:
		}
		<-d.release
		return d.fakeEmailDispatcher.SendEmail(ctx, in)
	}
	return "", unreachableDispatchErr("no responders")
}

func (d *outageMixDispatcher) attemptsFor(addr string) int {
	d.countMu.Lock()
	defer d.countMu.Unlock()
	return d.perAddr[addr]
}

// TestFanOutOutageLeavesInFlightDispatchUntouched asserts an outage declared
// while another dispatch is parked mid-flight neither cancels nor fails that
// dispatch: it completes and counts as sent, while a recipient that had not
// started is skipped.
//
// Determinism rests on the onOutageTripped seam, which fires after the trip is
// committed and before the tripping worker frees its slot: the parked dispatch
// is released only once the outage provably exists, so every later worker sees
// it. The skipped third recipient is what makes the outage load-bearing here —
// without it, a test of two always-slotted recipients passes whether or not
// the fail-fast works at all.
func TestFanOutOutageLeavesInFlightDispatchUntouched(t *testing.T) {
	email := &outageMixDispatcher{
		inflightAddr: "inflight@example.com",
		release:      make(chan struct{}),
		started:      make(chan struct{}, 1),
	}
	orch := newRetryOrchestrator(email)
	orch.concurrency = 2 // dead and inflight hold both slots; later must wait
	tripped := make(chan struct{})
	orch.onOutageTripped = func() { close(tripped) }

	type result struct {
		sent, failed int
		failures     []SendFailure
	}
	done := make(chan result, 1)
	go func() {
		s, f, fs := orch.fanOut(context.Background(), "p1", []model.CommitteeMember{
			{Email: "dead@example.com"},
			{Email: "inflight@example.com"},
			{Email: "later@example.com"},
		}, emailEnvelope{Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"})
		done <- result{s, f, fs}
	}()

	// Bounded waits: a regression in the trip must fail this test by name, not
	// hang until the package-wide timeout buries the signal in a goroutine dump.
	waitFor(t, email.started, "the in-flight dispatch to park")
	waitFor(t, tripped, "the outage to trip")
	close(email.release) // only now can it finish, provably after the trip
	got := <-done

	if got.sent != 1 || got.failed != 2 {
		t.Fatalf("sent=%d failed=%d, want 1/2 (the in-flight dispatch must still land)", got.sent, got.failed)
	}
	if n := email.attemptsFor("inflight@example.com"); n != 1 {
		t.Fatalf("in-flight recipient attempts=%d, want 1 (the outage must not retry, cancel, or re-drive it)", n)
	}
	if n := email.attemptsFor("dead@example.com"); n != dispatchMaxAttempts {
		t.Fatalf("tripping recipient attempts=%d, want %d", n, dispatchMaxAttempts)
	}
	// The load-bearing assertion: a worker that had not started when the
	// outage tripped spends no attempts and records the skip.
	if n := email.attemptsFor("later@example.com"); n != 0 {
		t.Fatalf("later recipient attempts=%d, want 0 (must skip once the outage tripped)", n)
	}
	var skipped, inflightFailed bool
	for _, f := range got.failures {
		if f.Email == "later@example.com" && f.Error == outageSkipReason {
			skipped = true
		}
		if f.Email == "inflight@example.com" {
			inflightFailed = true
		}
	}
	if !skipped {
		t.Fatalf("failures=%+v, want later@example.com recorded with %q", got.failures, outageSkipReason)
	}
	if inflightFailed {
		t.Fatalf("failures=%+v, want the in-flight recipient counted as sent, not failed", got.failures)
	}
}

// TestSendNewsletterOutageRevertsToDraft asserts the settle logic is untouched
// by the fail-fast: an outage that skips every recipient is still a total
// failure, so the row reverts to draft — the cleanly re-sendable state, and
// the reason stopping early beats dribbling out partial deliveries.
func TestSendNewsletterOutageRevertsToDraft(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "alice@example.com"}, {Email: "bob@example.com"}, {Email: "carol@example.com"}},
	}}
	email := &scriptedEmailDispatcher{alwaysErr: unreachableDispatchErr("no responders")}
	orch := newGatedOrchestrator(repo, committee, email)
	orch.retryBackoff = time.Millisecond
	orch.concurrency = 1

	draft := repo.addDraft("p1", []string{"c1"})
	if _, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID}); err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	orch.Drain(ctx)

	got := repo.get(draft.ID)
	if got.Status != model.StatusDraft {
		t.Fatalf("outage send settled to %q, want %q (retry path)", got.Status, model.StatusDraft)
	}
	if got.GroupID != nil || got.TotalRecipients != 0 || got.SentAt != nil {
		t.Fatalf("revert did not clear send-time fields: group_id=%v total=%d sent_at=%v", got.GroupID, got.TotalRecipients, got.SentAt)
	}
	// Only the tripping recipient dispatched; the rest were skipped.
	if n := email.attemptCount(); n != dispatchMaxAttempts {
		t.Fatalf("attempts=%d, want %d (outage must skip the undispatched recipients)", n, dispatchMaxAttempts)
	}
}

// TestDispatchWithRetryCtxCancelledOnUnreachableReportsNotExhausted pins the
// producer side of the `exhausted` signal for the cancellation route: a
// no-responders failure whose retries ctx cancellation cut short must not
// report exhaustion, since fanOut would otherwise be free to read it as proof
// of a systemic outage.
//
// This route is not reachable through fanOut deterministically (a cancelled
// ctx also stops the parent loop acquiring slots), so it is pinned here. The
// guard's effect at fan-out level is covered by
// TestFanOutOutageRequiresExhaustionNotMerelyUnreachable.
func TestDispatchWithRetryCtxCancelledOnUnreachableReportsNotExhausted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	email := &scriptedEmailDispatcher{
		alwaysErr: unreachableDispatchErr("no responders"),
		onFail:    func(int) { cancel() },
	}
	orch := newRetryOrchestrator(email)
	orch.retryBackoff = 30 * time.Second // cancellation must win, not the timer

	exhausted, err := orch.dispatchWithRetry(ctx, testSendInput("alice@example.com"))
	if !errors.Is(err, domain.ErrEmailServiceUnreachable) {
		t.Fatalf("err=%v, want the no-responders dispatch error", err)
	}
	if exhausted {
		t.Fatalf("exhausted=true after cancellation cut the retries short, want false (would trip the outage off one attempt)")
	}
	if got := email.attemptCount(); got != 1 {
		t.Fatalf("attempts=%d, want 1", got)
	}
}

// unreachableOnlyDispatcher fails failAddr with an error carrying
// ErrEmailServiceUnreachable but NOT ErrEmailNotDispatched — violating the
// "always accompanies" invariant documented on the sentinel. That combination
// is precisely what the fan-out's `exhausted` guard defends against: the retry
// gate returns such an error immediately (not retry-safe, so not exhausted),
// and without the guard the errors.Is check alone would declare a systemic
// outage off a single attempt.
type unreachableOnlyDispatcher struct {
	fakeEmailDispatcher
	failAddr string
	countMu  sync.Mutex
	perAddr  map[string]int
}

func (d *unreachableOnlyDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	d.countMu.Lock()
	if d.perAddr == nil {
		d.perAddr = make(map[string]int)
	}
	d.perAddr[in.To]++
	d.countMu.Unlock()
	if in.To == d.failAddr {
		return "", fmt.Errorf("unreachable but not tagged undispatched: %w", domain.ErrEmailServiceUnreachable)
	}
	return d.fakeEmailDispatcher.SendEmail(ctx, in)
}

func (d *unreachableOnlyDispatcher) attemptsFor(addr string) int {
	d.countMu.Lock()
	defer d.countMu.Unlock()
	return d.perAddr[addr]
}

// TestFanOutOutageRequiresExhaustionNotMerelyUnreachable pins the `exhausted`
// guard at the fan-out level: an unreachable-tagged failure that never spent
// its attempt budget must not declare an outage. Without the guard, one
// non-retry-safe failure would skip every remaining recipient of a send that
// email-service is answering perfectly well.
func TestFanOutOutageRequiresExhaustionNotMerelyUnreachable(t *testing.T) {
	email := &unreachableOnlyDispatcher{failAddr: "bad@example.com"}
	orch := newRetryOrchestrator(email)
	orch.concurrency = 1 // serialize: "later" is dispatched strictly after "bad"

	sent, failed, failures := orch.fanOut(context.Background(), "p1", []model.CommitteeMember{
		{Email: "bad@example.com"},
		{Email: "later@example.com"},
	}, emailEnvelope{Subject: "s", HTML: "<p>b</p>", Text: "b", GroupID: "g1"})

	// Not retry-safe, so it fails on the first attempt and never exhausts.
	if n := email.attemptsFor("bad@example.com"); n != 1 {
		t.Fatalf("failing recipient attempts=%d, want 1", n)
	}
	// The load-bearing assertion: the guard rejects the trip, so the next
	// recipient is still dispatched rather than skipped.
	if n := email.attemptsFor("later@example.com"); n != 1 {
		t.Fatalf("later recipient attempts=%d, want 1 (a non-exhausted unreachable failure must not trip the outage)", n)
	}
	if sent != 1 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, want 1/1", sent, failed)
	}
	for _, f := range failures {
		if f.Error == outageSkipReason {
			t.Fatalf("recipient %s was skipped for an outage that must not have tripped", f.Email)
		}
	}
}

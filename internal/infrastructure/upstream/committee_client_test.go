// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/auth"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

const testBearer = "test-jwt-token"

func ctxWithBearer(t *testing.T) context.Context {
	t.Helper()
	return auth.WithBearer(context.Background(), testBearer)
}

func TestListMembersValidatesCommitteeUID(t *testing.T) {
	c := NewCommitteeClient("http://example.invalid")
	if _, err := c.ListMembers(ctxWithBearer(t), ""); err == nil {
		t.Fatal("expected validation error for empty committee uid")
	} else {
		var v pkgerrors.Validation
		if !errors.As(err, &v) {
			t.Fatalf("expected Validation error, got %T: %v", err, err)
		}
	}
}

func TestListMembersRequiresBearer(t *testing.T) {
	c := NewCommitteeClient("http://example.invalid")
	_, err := c.ListMembers(context.Background(), "cmt-1")
	if err == nil {
		t.Fatal("expected error when bearer missing from context")
	}
	var u pkgerrors.Unexpected
	if !errors.As(err, &u) {
		t.Fatalf("expected Unexpected error, got %T: %v", err, err)
	}
}

func TestListMembersSinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != queryResourcesPath {
			t.Errorf("unexpected path: %s", got)
		}
		q := r.URL.Query()
		if q.Get("v") != queryAPIVersion {
			t.Errorf("missing v param: %s", q.Get("v"))
		}
		if q.Get("type") != "committee_member" {
			t.Errorf("wrong type: %s", q.Get("type"))
		}
		if q.Get("tags") != "committee_uid:cmt-1" {
			t.Errorf("wrong tags: %s", q.Get("tags"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearer {
			t.Errorf("wrong auth header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"resources":[
			{"type":"committee_member","id":"m1","data":{"email":"a@example.com","first_name":"Ada"}},
			{"type":"committee_member","id":"m2","data":{"email":"b@example.com","first_name":"Bob","last_name":"X"}}
		]}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	members, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if members[0].Email != "a@example.com" || members[0].FirstName != "Ada" {
		t.Errorf("unexpected first member: %+v", members[0])
	}
	if members[1].Email != "b@example.com" || members[1].FirstName != "Bob" {
		t.Errorf("unexpected second member: %+v", members[1])
	}
}

func TestListMembersPaginates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		token := r.URL.Query().Get("page_token")
		w.Header().Set("Content-Type", "application/json")
		switch token {
		case "":
			_, _ = fmt.Fprint(w, `{"resources":[
				{"type":"committee_member","id":"m1","data":{"email":"a@example.com","first_name":"Ada"}}
			],"page_token":"tkn-2"}`)
		case "tkn-2":
			_, _ = fmt.Fprint(w, `{"resources":[
				{"type":"committee_member","id":"m2","data":{"email":"b@example.com","first_name":"Bob"}}
			],"page_token":"tkn-3"}`)
		case "tkn-3":
			_, _ = fmt.Fprint(w, `{"resources":[
				{"type":"committee_member","id":"m3","data":{"email":"c@example.com","first_name":"Cleo"}}
			]}`)
		default:
			t.Errorf("unexpected page_token: %s", token)
		}
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	members, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(members))
	}
	if members[0].Email != "a@example.com" || members[1].Email != "b@example.com" || members[2].Email != "c@example.com" {
		t.Errorf("members out of order: %+v", members)
	}
}

func TestListMembersEmptyResultReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"resources":[]}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	members, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("expected 0 members, got %d", len(members))
	}
}

func TestListMembersMapsBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"message":"invalid tag"}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	_, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err == nil {
		t.Fatal("expected error")
	}
	var v pkgerrors.Validation
	if !errors.As(err, &v) {
		t.Fatalf("expected Validation, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "invalid tag") {
		t.Errorf("error did not surface server message: %v", err)
	}
}

func TestListMembersMapsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"message":"bad token"}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	_, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err == nil {
		t.Fatal("expected error")
	}
	var u pkgerrors.Unexpected
	if !errors.As(err, &u) {
		t.Fatalf("expected Unexpected, got %T: %v", err, err)
	}
}

func TestListMembersMapsServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"message":"down"}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	_, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err == nil {
		t.Fatal("expected error")
	}
	var s pkgerrors.ServiceUnavailable
	if !errors.As(err, &s) {
		t.Fatalf("expected ServiceUnavailable, got %T: %v", err, err)
	}
}

func TestListMembersMapsInternalServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	c := NewCommitteeClient(srv.URL)
	_, err := c.ListMembers(ctxWithBearer(t), "cmt-1")
	if err == nil {
		t.Fatal("expected error")
	}
	var u pkgerrors.Unexpected
	if !errors.As(err, &u) {
		t.Fatalf("expected Unexpected, got %T: %v", err, err)
	}
}

func TestBuildURL(t *testing.T) {
	c := NewCommitteeClient("http://example.invalid/")
	got := c.buildURL("abc-123", "")
	want := "http://example.invalid/query/resources?page_size=200&tags=committee_uid%3Aabc-123&type=committee_member&v=1"
	if got != want {
		t.Errorf("buildURL mismatch:\n got: %s\nwant: %s", got, want)
	}

	gotWithToken := c.buildURL("abc-123", "next")
	if !strings.Contains(gotWithToken, "page_token=next") {
		t.Errorf("expected page_token in URL: %s", gotWithToken)
	}
}

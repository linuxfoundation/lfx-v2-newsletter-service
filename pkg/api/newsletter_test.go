// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOptionalLayout_TriState pins the three JSON states of the update
// request's body_layout field: absent (preserve), explicit null (clear), and
// an object (replace).
func TestOptionalLayout_TriState(t *testing.T) {
	var absent UpdateNewsletterRequest
	if err := json.Unmarshal([]byte(`{"subject":"s"}`), &absent); err != nil {
		t.Fatalf("absent: %v", err)
	}
	if absent.BodyLayout.Present {
		t.Error("absent key must decode with Present=false")
	}

	var null UpdateNewsletterRequest
	if err := json.Unmarshal([]byte(`{"subject":"s","body_layout":null}`), &null); err != nil {
		t.Fatalf("null: %v", err)
	}
	if !null.BodyLayout.Present || null.BodyLayout.Layout != nil {
		t.Errorf("explicit null must decode Present=true, Layout=nil; got %+v", null.BodyLayout)
	}

	var obj UpdateNewsletterRequest
	if err := json.Unmarshal([]byte(`{"subject":"s","body_layout":{"wrapper_key":"default","blocks":[]}}`), &obj); err != nil {
		t.Fatalf("object: %v", err)
	}
	if !obj.BodyLayout.Present || obj.BodyLayout.Layout == nil || obj.BodyLayout.Layout.WrapperKey != "default" {
		t.Errorf("object must decode Present=true with the layout; got %+v", obj.BodyLayout)
	}
}
func TestOptionalLayout_ZeroValueOmitted(t *testing.T) {
	b, err := json.Marshal(UpdateNewsletterRequest{Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "body_layout") {
		t.Errorf("zero-value OptionalLayout must be omitted, got %s", b)
	}
	b2, _ := json.Marshal(UpdateNewsletterRequest{Subject: "s", BodyLayout: OptionalLayout{Present: true}})
	if !strings.Contains(string(b2), `"body_layout":null`) {
		t.Errorf("explicit null must marshal as null, got %s", b2)
	}
}

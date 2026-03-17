// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package ghost

import "testing"

func TestBuildTagFilterWithSpaces(t *testing.T) {
	filter := buildTagFilter("Weekly ED Newsletter")
	expected := "tag:'Weekly ED Newsletter',tag:weekly-ed-newsletter,tag:weeklyednewsletter"
	if filter != expected {
		t.Fatalf("unexpected filter: %s", filter)
	}
}

func TestBuildTagFilterSimpleSlug(t *testing.T) {
	filter := buildTagFilter("weeklyednewsletter")
	expected := "tag:'weeklyednewsletter',tag:weeklyednewsletter"
	if filter != expected {
		t.Fatalf("unexpected filter: %s", filter)
	}
}

func TestBuildTagFilterEscapesQuote(t *testing.T) {
	filter := buildTagFilter("LFX's Update")
	expected := "tag:'LFX\\'s Update',tag:lfx-s-update,tag:lfxsupdate"
	if filter != expected {
		t.Fatalf("unexpected filter: %s", filter)
	}
}

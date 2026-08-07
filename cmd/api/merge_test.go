package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestReplaceTagIsExactMatchOnly(t *testing.T) {
	// ParcelID is a prefix of SiteParcelID. The old bare-key fallback replaced
	// the substring anywhere, so whichever key Go's randomized map order
	// reached first corrupted the other -- differently on every run.
	xml := `<w:t>{{ParcelID}}</w:t><w:t>{{SiteParcelID}}</w:t>`
	got := replaceTag(xml, "ParcelID", "10-123-456")
	want := `<w:t>10-123-456</w:t><w:t>{{SiteParcelID}}</w:t>`
	if got != want {
		t.Fatalf("prefix collision:\n got %q\nwant %q", got, want)
	}
}

func TestReplaceTagOrderIndependent(t *testing.T) {
	// Applying the colliding keys in either order must converge on the same
	// document, which is what makes generation deterministic.
	const xml = `{{ParcelID}}|{{SiteParcelID}}`
	forward := replaceTag(replaceTag(xml, "ParcelID", "A"), "SiteParcelID", "B")
	reverse := replaceTag(replaceTag(xml, "SiteParcelID", "B"), "ParcelID", "A")
	if forward != reverse {
		t.Fatalf("order-dependent output: forward=%q reverse=%q", forward, reverse)
	}
	if forward != "A|B" {
		t.Fatalf("got %q, want %q", forward, "A|B")
	}
}

func TestReplaceTagAcceptsAlreadyWrappedKeys(t *testing.T) {
	// The compiler sometimes emits a key with its own braces attached.
	if got := replaceTag(`x {{SiteAcres}} y`, "{{SiteAcres}}", "1.7 Acres"); got != "x 1.7 Acres y" {
		t.Fatalf("wrapped key not normalized: %q", got)
	}
}

func TestFindUnreplacedTags(t *testing.T) {
	xml := `<w:t>{{Alpha}}</w:t> filled <w:t>{{Beta}}</w:t> <w:t>{{Alpha}}</w:t>`
	got := findUnreplacedTags(xml)
	want := []string{"Alpha", "Beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (deduped and sorted)", got, want)
	}
	if len(findUnreplacedTags(`<w:t>all filled in</w:t>`)) != 0 {
		t.Fatal("clean document must report no unreplaced tags")
	}
}

func TestInjectFieldDefaultsCarriesNoClientHardcodes(t *testing.T) {
	// Every generated report was pinned to one client's details regardless of
	// the actual project. Guard against reintroduction.
	out := injectFieldDefaults(`{"SiteStreetAddress":"400 Providence Rd"}`, map[string]string{})
	for _, banned := range []string{"Arkan", "Morningpark", "Hashem", "Gwinnett", "Roswell"} {
		if strings.Contains(out, banned) {
			t.Errorf("client hardcode %q leaked back into injectFieldDefaults output: %s", banned, out)
		}
	}
	if !strings.Contains(out, "400 Providence Rd") {
		t.Errorf("model-supplied address was overwritten: %s", out)
	}
}

func TestInjectFieldDefaultsAppliesEPAnswers(t *testing.T) {
	out := injectFieldDefaults(`{}`, map[string]string{
		"project_number": "MEG-302858",
		"parcel_id":      "11-0022-33",
		"site_acreage":   "4.2 Acres",
	})
	for _, want := range []string{`"ProjectNo":"302858"`, `"ParcelID":"11-0022-33"`, `"SiteAcreage":"4.2 Acres"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	// The cover-letter text boxes must stay blanked (page-2 layout hack).
	if !strings.Contains(out, `"Proposal_Letter1":""`) {
		t.Errorf("Proposal_Letter blanking lost: %s", out)
	}
}

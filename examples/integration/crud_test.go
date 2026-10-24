// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/the-protobuf-project/service/examples/generated/go/acme/directory/v1"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/directory"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/directory/v1/directoryv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/servicex"
)

func organizations(t *testing.T) *directoryv1svc.OrganizationsServer {
	repos := directory.New(open(t))
	return directoryv1svc.NewOrganizationsServer(directoryv1svc.OrganizationsRepositories{Organizations: repos.Organizations}, nil)
}

// TestCreateGet covers AIP-133 and AIP-131: a chosen id, a minted one, the
// checks before the write, and what the server owns.
func TestCreateGet(t *testing.T) {
	s, c := organizations(t), ctx(t)

	org := must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{
		OrganizationId: "acme",
		Organization:   &directoryv1.Organization{DisplayName: "Acme", Etag: "client-sent", Registration: "R1"},
	}))
	if org.GetName() != "organizations/acme" || org.GetEtag() == "client-sent" || org.GetCreateTime() == nil {
		t.Fatalf("created %v: want the chosen name, a server etag and a create_time", org)
	}
	minted := must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{Organization: &directoryv1.Organization{DisplayName: "Minted"}}))
	if minted.GetName() == "" {
		t.Fatal("an empty id should have had one minted")
	}

	_, err := s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &directoryv1.Organization{DisplayName: "Again"}})
	st := wantStatus(t, err, codes.AlreadyExists, servicex.ReasonAlreadyExists)
	if domainOf(st) != "directory.acme.example" {
		t.Errorf("ErrorInfo domain %q, want the service's default host", domainOf(st))
	}
	_, err = s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "Not_Valid", Organization: &directoryv1.Organization{DisplayName: "x"}})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonInvalidID)
	_, err = s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{Organization: &directoryv1.Organization{}})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonRequiredField)

	preview := must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "dry", ValidateOnly: true, Organization: &directoryv1.Organization{DisplayName: "Dry"}}))
	if preview.GetName() != "organizations/dry" {
		t.Fatalf("validate_only preview %v", preview)
	}
	_, err = s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "organizations/dry"})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)

	got := must(s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "organizations/acme"}))
	if got.GetDisplayName() != "Acme" {
		t.Fatalf("got %v", got)
	}
	_, err = s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "calendars/acme"})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonMalformedName)
}

// TestListPaging covers AIP-132: pages that add up, a filter, an order.
func TestListPaging(t *testing.T) {
	s, c := organizations(t), ctx(t)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: id, Organization: &directoryv1.Organization{DisplayName: "Org " + id}}))
	}
	var seen []string
	pages := 0
	for token := ""; ; {
		page := must(s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{PageSize: 2, PageToken: token, OrderBy: "display_name desc"}))
		pages++
		for _, o := range page.GetOrganizations() {
			seen = append(seen, o.GetName())
		}
		if token = page.GetNextPageToken(); token == "" {
			break
		}
	}
	if pages != 3 || len(seen) != 5 || seen[0] != "organizations/e" {
		t.Fatalf("%d pages of %v, want 3 pages of 5 in descending order", pages, seen)
	}
	filtered := must(s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{Filter: `display_name = "Org c"`}))
	if len(filtered.GetOrganizations()) != 1 {
		t.Fatalf("filter returned %v", filtered.GetOrganizations())
	}
	_, err := s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{Filter: `no_such_field = 1`})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonInvalidArgument)
	_, err = s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{PageSize: -1})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonInvalidArgument)
}

// TestUpdate covers AIP-134: masks, IMMUTABLE fields, etags, validate_only
// and allow_missing.
func TestUpdate(t *testing.T) {
	s, c := organizations(t), ctx(t)
	org := must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &directoryv1.Organization{DisplayName: "Acme", Registration: "R1"}}))

	up := must(s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: org.GetName(), DisplayName: "Acme Ltd", Etag: org.GetEtag()},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"displayName"}}, // a JSON name, as REST sends it
	}))
	if up.GetDisplayName() != "Acme Ltd" || up.GetRegistration() != "R1" || up.GetEtag() == org.GetEtag() {
		t.Fatalf("updated %v", up)
	}

	_, err := s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: org.GetName(), DisplayName: "Stale", Etag: org.GetEtag()},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	})
	wantStatus(t, err, codes.Aborted, servicex.ReasonConcurrentModification)

	_, err = s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: org.GetName(), Registration: "R2"},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"registration"}},
	})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonImmutableField)

	_, err = s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: org.GetName()},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"no_such_field"}},
	})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonUnknownMaskPath)

	_, err = s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{Organization: &directoryv1.Organization{Name: org.GetName()}})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonNothingToUpdate)

	preview := must(s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: org.GetName(), DisplayName: "Preview"},
		ValidateOnly: true,
	}))
	stored := must(s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: org.GetName()}))
	if preview.GetDisplayName() != "Preview" || stored.GetDisplayName() != "Acme Ltd" {
		t.Fatalf("validate_only wrote: preview %q, stored %q", preview.GetDisplayName(), stored.GetDisplayName())
	}

	created := must(s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: "organizations/upserted", DisplayName: "Upserted"},
		AllowMissing: true,
	}))
	if created.GetName() != "organizations/upserted" {
		t.Fatalf("allow_missing created %v", created)
	}
	_, err = s.UpdateOrganization(c, &directoryv1.UpdateOrganizationRequest{
		Organization: &directoryv1.Organization{Name: "organizations/missing", DisplayName: "x"},
	})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)
}

// TestDeleteUndelete covers AIP-135 and AIP-164: soft delete, show_deleted,
// undelete, allow_missing and the etag guard.
func TestDeleteUndelete(t *testing.T) {
	s, c := organizations(t), ctx(t)
	org := must(s.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &directoryv1.Organization{DisplayName: "Acme"}}))

	_, err := s.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: org.GetName(), Etag: "stale"})
	wantStatus(t, err, codes.Aborted, servicex.ReasonConcurrentModification)
	must(s.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: org.GetName(), ValidateOnly: true}))
	if got := must(s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: org.GetName()})); got.GetDeleteTime() != nil {
		t.Fatal("validate_only deleted")
	}
	must(s.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: org.GetName(), Etag: org.GetEtag()}))

	got := must(s.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: org.GetName()}))
	if got.GetDeleteTime() == nil {
		t.Fatal("a soft delete keeps the record, with a delete_time")
	}
	if n := len(must(s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{})).GetOrganizations()); n != 0 {
		t.Fatalf("List shows %d deleted organizations", n)
	}
	if n := len(must(s.ListOrganizations(c, &directoryv1.ListOrganizationsRequest{ShowDeleted: true})).GetOrganizations()); n != 1 {
		t.Fatalf("show_deleted shows %d", n)
	}
	_, err = s.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: org.GetName()})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)
	must(s.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: "organizations/never", AllowMissing: true}))

	back := must(s.UndeleteOrganization(c, &directoryv1.UndeleteOrganizationRequest{Name: org.GetName()}))
	if back.GetDeleteTime() != nil {
		t.Fatal("undelete left delete_time")
	}
	_, err = s.UndeleteOrganization(c, &directoryv1.UndeleteOrganizationRequest{Name: org.GetName()})
	wantStatus(t, err, codes.AlreadyExists, servicex.ReasonAlreadyExists)
}

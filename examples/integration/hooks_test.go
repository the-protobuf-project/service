// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/calendar/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/directory/v1"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/calendar"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/directory"
	"github.com/the-protobuf-project/service/examples/generated/repository/repox"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/calendar/v1/calendarv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/directory/v1/directoryv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/servicex"
)

// siteHooks is freebusy's tenancy rule as hooks: a site belongs to a live
// organization, and a site that still "has resources" cannot be deleted. It
// is all the hand-written code an organizational unit server needs.
type siteHooks struct {
	directoryv1svc.NopOrganizationalUnitsHooks
	orgs directory.OrganizationRepository
}

func (h siteHooks) ValidateOrganizationalUnit(ctx context.Context, _ servicex.Op, m *directoryv1.OrganizationalUnit, paths []string) error {
	if !servicex.Touches(paths, "organization") {
		return nil
	}
	org, err := h.orgs.Get(ctx, m.GetOrganization())
	switch {
	case servicex.IsNotFound(err):
		return servicex.Missing("ORGANIZATION_NOT_FOUND", "directory.acme.example/Organization", m.GetOrganization(),
			fmt.Sprintf("organization %q does not exist", m.GetOrganization()), "Create it first with CreateOrganization")
	case err != nil:
		return err
	case org.GetDeleteTime() != nil:
		return servicex.Blocked("ORGANIZATION_DELETED", "directory.acme.example/Organization", m.GetOrganization(),
			fmt.Sprintf("organization %q is deleted", m.GetOrganization()), "Restore it with UndeleteOrganization")
	}
	return nil
}

func (siteHooks) BeforeDeleteOrganizationalUnit(_ context.Context, name string, _ bool) error {
	if name == "organizationalUnits/busy" {
		return servicex.Blocked("SITE_HAS_RESOURCES", "directory.acme.example/OrganizationalUnit", name,
			"organizational unit "+name+" still has resources", "Delete or move them first")
	}
	return nil
}

func TestHooks(t *testing.T) {
	repos, c := directory.New(open(t)), ctx(t)
	orgs := directoryv1svc.NewOrganizationsServer(directoryv1svc.OrganizationsRepositories{Organizations: repos.Organizations}, nil)
	sites := directoryv1svc.NewOrganizationalUnitsServer(
		directoryv1svc.OrganizationalUnitsRepositories{OrganizationalUnits: repos.OrganizationalUnits},
		siteHooks{orgs: repos.Organizations})

	_, err := sites.CreateOrganizationalUnit(c, &directoryv1.CreateOrganizationalUnitRequest{
		OrganizationalUnitId: "riverside",
		OrganizationalUnit:   &directoryv1.OrganizationalUnit{DisplayName: "Riverside", Organization: "organizations/none"},
	})
	wantStatus(t, err, codes.NotFound, "ORGANIZATION_NOT_FOUND")

	must(orgs.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &directoryv1.Organization{DisplayName: "Acme"}}))
	for _, id := range []string{"riverside", "busy"} {
		must(sites.CreateOrganizationalUnit(c, &directoryv1.CreateOrganizationalUnitRequest{
			OrganizationalUnitId: id,
			OrganizationalUnit:   &directoryv1.OrganizationalUnit{DisplayName: id, Organization: "organizations/acme"},
		}))
	}
	_, err = sites.DeleteOrganizationalUnit(c, &directoryv1.DeleteOrganizationalUnitRequest{Name: "organizationalUnits/busy"})
	wantStatus(t, err, codes.FailedPrecondition, "SITE_HAS_RESOURCES")

	must(orgs.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: "organizations/acme"}))
	_, err = sites.UpdateOrganizationalUnit(c, &directoryv1.UpdateOrganizationalUnitRequest{
		OrganizationalUnit: &directoryv1.OrganizationalUnit{Name: "organizationalUnits/riverside", Organization: "organizations/acme"},
	})
	wantStatus(t, err, codes.FailedPrecondition, "ORGANIZATION_DELETED")
	// A mask that leaves the organization alone does not consult it.
	must(sites.UpdateOrganizationalUnit(c, &directoryv1.UpdateOrganizationalUnitRequest{
		OrganizationalUnit: &directoryv1.OrganizationalUnit{Name: "organizationalUnits/riverside", DisplayName: "Riverside Court"},
	}))
}

// calendars embeds the generated server and writes what it leaves: the
// custom QueryFreeBusy, and a DeleteCalendar that honours force -- the
// override of a standard method.
type calendars struct {
	*calendarv1svc.CalendarsServer
	events calendar.EventRepository
}

func (s *calendars) QueryFreeBusy(ctx context.Context, req *calendarv1.QueryFreeBusyRequest) (*calendarv1.QueryFreeBusyResponse, error) {
	out := &calendarv1.QueryFreeBusyResponse{}
	err := s.Options().Run(ctx, "Calendars", "QueryFreeBusy", func(ctx context.Context) error {
		page, _, err := s.events.List(ctx, req.GetName(), repox.ListInput{PageSize: 1000})
		out.Busy = int32(len(page))
		return err
	})
	return out, err
}

// eventHooks refuses events under a calendar that is missing or deleted:
// freebusy's liveParent, as a ParentCheck hook.
type eventHooks struct {
	calendarv1svc.NopEventsHooks
	calendars calendar.CalendarRepository
}

func (h eventHooks) ParentCheckEvent(ctx context.Context, op servicex.Op, parent string) error {
	if op == servicex.OpList {
		return nil
	}
	cal, err := h.calendars.Get(ctx, parent)
	if err != nil {
		return servicex.NotFoundAs(err, "calendar.acme.example/Calendar", parent)
	}
	if cal.GetDeleteTime() != nil {
		return servicex.Blocked("CALENDAR_DELETED", "calendar.acme.example/Calendar", parent, "calendar "+parent+" is deleted", "Restore it first")
	}
	return nil
}

// calendarHooks refuses to delete a calendar with events unless forced.
type calendarHooks struct {
	calendarv1svc.NopCalendarsHooks
	events calendar.EventRepository
}

func (h calendarHooks) BeforeDeleteCalendar(ctx context.Context, name string, force bool) error {
	page, _, err := h.events.List(ctx, name, repox.ListInput{PageSize: 1})
	if err != nil {
		return err
	}
	if len(page) > 0 && !force {
		return servicex.Blocked("CALENDAR_NOT_EMPTY", "calendar.acme.example/Calendar", name, "calendar "+name+" still has events", "Send force=true")
	}
	return nil
}

// TestOverGRPC serves the calendar package through RegisterGRPC on a real
// gRPC server, so the statuses are checked as a client receives them: an
// embedded server with a custom method, hooks on a nested resource, AIP-159
// wildcards, the server's own translator, and Purge.
func TestOverGRPC(t *testing.T) {
	repos, c := calendar.New(open(t)), ctx(t)
	translator := servicex.NewTranslator("calendar.acme.example").Declare(problem.Reason{Name: "CALENDAR_NOT_EMPTY", Code: codes.FailedPrecondition})
	cals := &calendars{
		CalendarsServer: calendarv1svc.NewCalendarsServer(calendarv1svc.CalendarsRepositories{Calendars: repos.Calendars},
			calendarHooks{events: repos.Events}, servicex.WithTranslator(translator)),
		events: repos.Events,
	}
	evs := calendarv1svc.NewEventsServer(calendarv1svc.EventsRepositories{Events: repos.Events}, eventHooks{calendars: repos.Calendars})

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	calendarv1svc.RegisterGRPC(srv, calendarv1svc.Servers{Calendars: cals, Events: evs})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	calc, evc := calendarv1.NewCalendarsClient(conn), calendarv1.NewEventsClient(conn)

	must(calc.CreateCalendar(c, &calendarv1.CreateCalendarRequest{CalendarId: "rooms", Calendar: &calendarv1.Calendar{DisplayName: "Rooms"}}))
	must(calc.CreateCalendar(c, &calendarv1.CreateCalendarRequest{CalendarId: "desks", Calendar: &calendarv1.Calendar{DisplayName: "Desks"}}))
	for i, parent := range []string{"calendars/rooms", "calendars/rooms", "calendars/desks"} {
		must(evc.CreateEvent(c, &calendarv1.CreateEventRequest{Parent: parent, EventId: fmt.Sprintf("e%d", i),
			Event: &calendarv1.Event{Summary: "busy", StartTime: timestamppb.Now()}}))
	}
	_, err = evc.CreateEvent(c, &calendarv1.CreateEventRequest{Parent: "calendars/none", Event: &calendarv1.Event{Summary: "x", StartTime: timestamppb.Now()}})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)
	_, err = evc.CreateEvent(c, &calendarv1.CreateEventRequest{Parent: "rooms", Event: &calendarv1.Event{Summary: "x", StartTime: timestamppb.Now()}})
	wantStatus(t, err, codes.InvalidArgument, servicex.ReasonMalformedName)

	if n := len(must(evc.ListEvents(c, &calendarv1.ListEventsRequest{Parent: "calendars/rooms"})).GetEvents()); n != 2 {
		t.Fatalf("rooms has %d events, want 2", n)
	}
	if n := len(must(evc.ListEvents(c, &calendarv1.ListEventsRequest{Parent: "calendars/-"})).GetEvents()); n != 3 {
		t.Fatalf("calendars/- lists %d events, want 3", n)
	}
	if busy := must(calc.QueryFreeBusy(c, &calendarv1.QueryFreeBusyRequest{Name: "calendars/rooms"})).GetBusy(); busy != 2 {
		t.Fatalf("custom QueryFreeBusy says %d", busy)
	}
	_, err = evc.ExpandEvent(c, &calendarv1.ExpandEventRequest{Name: "calendars/rooms/events/e0"})
	if st, _ := grpcStatus(err); st != codes.Unimplemented {
		t.Fatalf("ExpandEvent, never written, should be UNIMPLEMENTED; got %v", err)
	}

	_, err = calc.DeleteCalendar(c, &calendarv1.DeleteCalendarRequest{Name: "calendars/rooms"})
	st := wantStatus(t, err, codes.FailedPrecondition, "CALENDAR_NOT_EMPTY")
	if domainOf(st) != "calendar.acme.example" {
		t.Errorf("domain %q", domainOf(st))
	}
	deleted := must(calc.DeleteCalendar(c, &calendarv1.DeleteCalendarRequest{Name: "calendars/rooms", Force: true}))
	if deleted.GetDeleteTime() == nil {
		t.Fatal("DeleteCalendar returns the soft-deleted calendar")
	}
	_, err = evc.CreateEvent(c, &calendarv1.CreateEventRequest{Parent: "calendars/rooms", Event: &calendarv1.Event{Summary: "x", StartTime: timestamppb.Now()}})
	wantStatus(t, err, codes.FailedPrecondition, "CALENDAR_DELETED")

	_, err = calc.UndeleteCalendar(c, &calendarv1.UndeleteCalendarRequest{Name: "calendars/rooms", Etag: "stale"})
	wantStatus(t, err, codes.Aborted, servicex.ReasonConcurrentModification)
	must(calc.UndeleteCalendar(c, &calendarv1.UndeleteCalendarRequest{Name: "calendars/rooms", Etag: deleted.GetEtag()}))

	page := must(calc.ListCalendars(c, &calendarv1.ListCalendarsRequest{ReadMask: nil}))
	if len(page.GetCalendars()) != 2 {
		t.Fatalf("calendars: %v", page.GetCalendars())
	}
}

func grpcStatus(err error) (codes.Code, string) {
	st, _ := statusOf(err)
	return st.Code(), st.Message()
}

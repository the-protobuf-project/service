// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/the-protobuf-project/service/examples/generated/go/acme/activity/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/booking/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/calendar/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/directory/v1"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/booking/v1/bookingv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/directory/v1/directoryv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/app/database"
	"github.com/the-protobuf-project/service/examples/generated/service/app/server"
	"github.com/the-protobuf-project/service/examples/generated/service/servicex"
)

// logic is the business logic acme's custom methods need: the application's
// half of the Provision, written over the Deps.
type logic struct{ repos server.Repositories }

func (logic) ReleaseHold(context.Context, *bookingv1.ReleaseHoldRequest) (*bookingv1.Hold, error) {
	return nil, servicex.Blocked(bookingv1svc.ReasonHoldNotActive, "booking.acme.example/Hold", "", "the hold is not active", "Create another")
}

func (logic) CreateReservation(context.Context, *bookingv1.CreateReservationRequest) (*bookingv1.Reservation, error) {
	return &bookingv1.Reservation{}, nil
}

// QueryFreeBusy fails with another package's sentinel, which the shell's
// translator renders because it knows every package's kinds.
func (l logic) QueryFreeBusy(ctx context.Context, req *calendarv1.QueryFreeBusyRequest) (*calendarv1.QueryFreeBusyResponse, error) {
	if req.GetName() == "calendars/full" {
		return nil, fmt.Errorf("calendar %s: %w", req.GetName(), bookingv1svc.ErrCapacityExhausted)
	}
	cal, err := l.repos.Calendar.Calendars.Get(ctx, req.GetName())
	if err != nil {
		return nil, servicex.NotFoundAs(err, "calendar.acme.example/Calendar", req.GetName())
	}
	return &calendarv1.QueryFreeBusyResponse{Busy: int32(len(cal.GetDisplayName()))}, nil
}

func (logic) ExpandEvent(context.Context, *calendarv1.ExpandEventRequest) (*calendarv1.ExpandEventResponse, error) {
	return &calendarv1.ExpandEventResponse{}, nil
}

// WatchCalendar streams two changes of the calendar, or refuses one that is
// not there with a problem the server's translator renders.
func (logic) WatchCalendar(req *activityv1.WatchCalendarRequest, stream grpc.ServerStreamingServer[activityv1.WatchCalendarResponse]) error {
	if req.GetCalendar() == "calendars/none" {
		return servicex.Blocked("CALENDAR_NOT_WATCHABLE", "calendar.acme.example/Calendar", req.GetCalendar(), "nothing to watch", "Watch a calendar that exists")
	}
	if stream.Context() == nil {
		return errors.New("the stream lost its context")
	}
	for _, change := range []string{"created", "updated"} {
		if err := stream.Send(&activityv1.WatchCalendarResponse{Event: req.GetCalendar() + "/events/e", Change: change}); err != nil {
			return err
		}
	}
	return nil
}

// ImportEvents counts the events the client sends, answering once it is done.
func (logic) ImportEvents(stream grpc.ClientStreamingServer[activityv1.ImportEventsRequest, activityv1.ImportEventsResponse]) error {
	n := int32(0)
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&activityv1.ImportEventsResponse{ImportedCount: n})
		}
		if err != nil {
			return err
		}
		n++
	}
}

// SyncEvents answers each change as it arrives with the count applied so far.
func (logic) SyncEvents(stream grpc.BidiStreamingServer[activityv1.SyncEventsRequest, activityv1.SyncEventsResponse]) error {
	n := int32(0)
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		n++
		if err := stream.Send(&activityv1.SyncEventsResponse{Calendar: req.GetCalendar(), AppliedCount: n}); err != nil {
			return err
		}
	}
}

// countingHooks counts the organizations created, through a provisioned hook.
type countingHooks struct {
	directoryv1svc.NopOrganizationsHooks
	created *int
}

func (h countingHooks) AfterCreateOrganization(context.Context, *directoryv1.Organization) error {
	*h.created++
	return nil
}

func provision(created *int) server.Provider {
	return func(d *server.Deps) (server.Provision, error) {
		l := logic{repos: d.Repos}
		return server.Provision{
			Hooks: server.Hooks{Organizations: countingHooks{created: created}},
			Logic: server.Logic{Holds: l, Reservations: l, Calendars: l, Events: l, Activity: l},
		}, nil
	}
}

// TestShellRefusesAnIncompleteProvision: a custom method with no Logic stops
// the assembly, naming every one missing, rather than serving UNIMPLEMENTED.
func TestShellRefusesAnIncompleteProvision(t *testing.T) {
	_, _, err := server.NewGRPCServer(&database.Connection{PgSQLConn: open(t).Gorm}, nil)
	if err == nil {
		t.Fatal("an empty provision was accepted")
	}
	for _, want := range []string{"Logic.Holds", "Logic.Reservations", "Logic.Calendars", "Logic.Events", "Logic.Activity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

// TestShellServes assembles acme through the generated shell -- repositories,
// hooks, logic, the run-wide translator, the interceptor chain and every
// package's registration -- and calls it as a client does.
func TestShellServes(t *testing.T) {
	created := 0
	srv, svc, err := server.NewGRPCServer(&database.Connection{PgSQLConn: open(t).Gorm}, provision(&created))
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := ctx(t)

	orgs := directoryv1.NewOrganizationsClient(conn)
	must(orgs.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &directoryv1.Organization{DisplayName: "Acme"}}))
	if created != 1 {
		t.Fatalf("the provisioned hook ran %d times", created)
	}
	// The run-wide translator renders the generated servers' own reasons.
	_, err = orgs.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "organizations/none"})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)

	cals := calendarv1.NewCalendarsClient(conn)
	must(cals.CreateCalendar(c, &calendarv1.CreateCalendarRequest{CalendarId: "rooms", Calendar: &calendarv1.Calendar{DisplayName: "Rooms"}}))
	if busy := must(cals.QueryFreeBusy(c, &calendarv1.QueryFreeBusyRequest{Name: "calendars/rooms"})).GetBusy(); busy != 5 {
		t.Fatalf("the Logic's QueryFreeBusy says %d", busy)
	}
	_, err = cals.QueryFreeBusy(c, &calendarv1.QueryFreeBusyRequest{Name: "calendars/full"})
	st := wantStatus(t, err, codes.FailedPrecondition, bookingv1svc.ReasonCapacityExhausted)
	if domainOf(st) != "calendar.acme.example" {
		t.Errorf("domain %q, want the Calendars service's", domainOf(st))
	}

	holds := bookingv1.NewHoldsClient(conn)
	_, err = holds.ReleaseHold(c, &bookingv1.ReleaseHoldRequest{Name: "holds/h"})
	wantStatus(t, err, codes.FailedPrecondition, bookingv1svc.ReasonHoldNotActive)

	if svc.Calendars == nil || svc.Dispatcher == nil || svc.Purger == nil {
		t.Fatal("the assembly left a server, the dispatcher or the purger unbuilt")
	}
}

// TestShellPurges: the generated purger removes a record soft-deleted longer
// ago than the retention, and leaves a live one.
func TestShellPurges(t *testing.T) {
	_, svc, err := server.NewGRPCServer(&database.Connection{PgSQLConn: open(t).Gorm}, provision(new(int)))
	if err != nil {
		t.Fatal(err)
	}
	c, orgs := ctx(t), svc.Organizations
	must(orgs.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "gone", Organization: &directoryv1.Organization{DisplayName: "Gone"}}))
	must(orgs.CreateOrganization(c, &directoryv1.CreateOrganizationRequest{OrganizationId: "kept", Organization: &directoryv1.Organization{DisplayName: "Kept"}}))
	must(orgs.DeleteOrganization(c, &directoryv1.DeleteOrganizationRequest{Name: "organizations/gone"}))

	n, err := server.NewPurger(svc.Repos, 0, nil).Sweep(c)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d records, want 1", n)
	}
	_, err = orgs.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "organizations/gone"})
	wantStatus(t, err, codes.NotFound, servicex.ReasonNotFound)
	must(orgs.GetOrganization(c, &directoryv1.GetOrganizationRequest{Name: "organizations/kept"}))
}

// TestShellStreams serves the streaming service through the shell and calls
// every shape as a client does: the server's, the client's and both, and a
// stream that fails with a problem the translator renders in the service's
// domain.
func TestShellStreams(t *testing.T) {
	srv, _, err := server.NewGRPCServer(&database.Connection{PgSQLConn: open(t).Gorm}, provision(new(int)))
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c, client := ctx(t), activityv1.NewActivityClient(conn)

	// Server streaming.
	watch := must(client.WatchCalendar(c, &activityv1.WatchCalendarRequest{Calendar: "calendars/rooms"}))
	var changes []string
	for {
		msg, err := watch.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, msg.GetChange())
	}
	if strings.Join(changes, ",") != "created,updated" {
		t.Fatalf("watched %v", changes)
	}
	failed := must(client.WatchCalendar(c, &activityv1.WatchCalendarRequest{Calendar: "calendars/none"}))
	_, err = failed.Recv()
	st := wantStatus(t, err, codes.FailedPrecondition, "CALENDAR_NOT_WATCHABLE")
	if domainOf(st) != "activity.acme.example" {
		t.Errorf("domain %q, want the Activity service's", domainOf(st))
	}

	// Client streaming.
	upload := must(client.ImportEvents(c))
	for i := range 3 {
		if err := upload.Send(&activityv1.ImportEventsRequest{Calendar: "calendars/rooms", Summary: fmt.Sprint("e", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := must(upload.CloseAndRecv()).GetImportedCount(); got != 3 {
		t.Fatalf("imported %d, want 3", got)
	}

	// Bidirectional streaming: each answer arrives before the next send.
	sync := must(client.SyncEvents(c))
	for i := int32(1); i <= 2; i++ {
		if err := sync.Send(&activityv1.SyncEventsRequest{Calendar: "calendars/rooms", Change: "moved"}); err != nil {
			t.Fatal(err)
		}
		if got := must(sync.Recv()).GetAppliedCount(); got != i {
			t.Fatalf("applied %d, want %d", got, i)
		}
	}
	if err := sync.CloseSend(); err != nil {
		t.Fatal(err)
	}
}

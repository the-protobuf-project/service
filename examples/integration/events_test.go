// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/the-protobuf-project/runtime-go/events"
	"github.com/the-protobuf-project/runtime-go/grpc/idempotency"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/booking/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/calendar/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/idempotency/v1"
	"github.com/the-protobuf-project/service/examples/generated/go/acme/webhook/v1"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/calendar"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/requestkeys"
	"github.com/the-protobuf-project/service/examples/generated/repository/acme/webhook"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/booking/v1/bookingv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/calendar/v1/calendarv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/idempotency/v1/idempotencyv1svc"
	"github.com/the-protobuf-project/service/examples/generated/service/acme/webhook/v1/webhookv1svc"
)

// TestEventTypes checks the event type a change is given: a declared state
// wins, and a resource with no state reports its lifecycle.
func TestEventTypes(t *testing.T) {
	now := timestamppb.Now()
	later := timestamppb.New(now.AsTime().Add(time.Minute))
	cases := []struct {
		got, want string
	}{
		{bookingv1svc.HoldEventType(&bookingv1.Hold{State: bookingv1.Hold_STATE_ACTIVE}), "example.acme.hold.active"},
		{bookingv1svc.HoldEventType(&bookingv1.Hold{State: bookingv1.Hold_STATE_RELEASED}), "example.acme.hold.let_go"},
		{bookingv1svc.HoldEventType(&bookingv1.Hold{}), ""},
		{bookingv1svc.ReservationEventType(&bookingv1.Reservation{CreateTime: now, UpdateTime: now}), "acme.booking.v1.reservation.created"},
		{bookingv1svc.ReservationEventType(&bookingv1.Reservation{CreateTime: now, UpdateTime: later}), "acme.booking.v1.reservation.updated"},
		{bookingv1svc.ReservationEventType(&bookingv1.Reservation{CreateTime: now, UpdateTime: later, DeleteTime: later}), "acme.booking.v1.reservation.deleted"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("event type %q, want %q", c.got, c.want)
		}
	}
	if n := len(bookingv1svc.EventTypes()); n != 6 {
		t.Errorf("booking declares %d event types, want 6: %v", n, bookingv1svc.EventTypes())
	}
}

// TestErrorReasons checks the translator generated from the proto's
// error_domain enum.
func TestErrorReasons(t *testing.T) {
	tr := bookingv1svc.NewTranslator()
	st := wantStatus(t, tr.ToStatus(errors.Join(bookingv1svc.ErrCapacityExhausted)), codes.FailedPrecondition, bookingv1svc.ReasonCapacityExhausted)
	if domainOf(st) != bookingv1svc.ErrorDomain {
		t.Errorf("domain %q", domainOf(st))
	}
	wantStatus(t, tr.ToStatus(bookingv1svc.NewHoldNotActive("booking.acme.example/Hold", "organizationalUnits/o/holds/h", "hold h is released")),
		codes.FailedPrecondition, bookingv1svc.ReasonHoldNotActive)
	wantStatus(t, tr.ToStatus(bookingv1svc.NewHoldNotFound("reservation.hold", "hold h does not exist")),
		codes.InvalidArgument, bookingv1svc.ReasonHoldNotFound)
	wantStatus(t, tr.ToStatus(bookingv1svc.ErrEngineBusy), codes.ResourceExhausted, bookingv1svc.ReasonEngineBusy)
	if !bookingv1svc.Retryable(bookingv1svc.ReasonEngineBusy) || bookingv1svc.Retryable(bookingv1svc.ReasonHoldNotActive) {
		t.Error("Retryable disagrees with the proto")
	}
}

// TestDispatcher runs runtime-go's webhook dispatcher end to end over the
// generated events.Store and feed: an event written through the generated
// server reaches a webhook as a signed CloudEvent.
func TestDispatcher(t *testing.T) {
	conn, c := open(t), ctx(t)
	cal, hooks := calendar.New(conn), webhook.New(conn)

	var (
		mu       sync.Mutex
		received []map[string]any
	)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev map[string]any
		_ = json.Unmarshal(body, &ev)
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(endpoint.Close)

	secret := must(events.NewSecret())
	must(hooks.Webhooks.Create(c, &webhookv1.Webhook{Name: "webhooks/w", Uri: endpoint.URL, Secret: secret, OrganizationalUnit: ""}))
	time.Sleep(20 * time.Millisecond)

	cals := calendarv1svc.NewCalendarsServer(calendarv1svc.CalendarsRepositories{Calendars: cal.Calendars}, nil)
	evs := calendarv1svc.NewEventsServer(calendarv1svc.EventsRepositories{Events: cal.Events}, nil)
	must(cals.CreateCalendar(c, &calendarv1.CreateCalendarRequest{CalendarId: "rooms", Calendar: &calendarv1.Calendar{DisplayName: "Rooms"}}))
	must(evs.CreateEvent(c, &calendarv1.CreateEventRequest{Parent: "calendars/rooms", EventId: "standup",
		Event: &calendarv1.Event{Summary: "Standup", StartTime: timestamppb.Now()}}))
	time.Sleep(20 * time.Millisecond)

	store := &webhookv1svc.EventStore{Webhooks: hooks.Webhooks, Deliveries: hooks.Deliveries, Cursors: hooks.FeedCursors}
	d := events.NewDispatcher(store, []events.Feed{calendarv1svc.EventFeed(cal.Events)}, events.Options{
		Source:               "//calendar.acme.example",
		AllowPrivateNetworks: true,
		HookSettle:           time.Millisecond,
		Lag:                  time.Second,
		// The cursor of a feed never read starts at now; begin it before the
		// writes above so the round reads them.
		Now: func() time.Time { return time.Now() },
	})
	// Start the feed's cursor before the writes, as a dispatcher running all
	// along would have.
	if _, err := store.CreateCursor(c, events.Cursor{Feed: "example.acme.calendar.event", Position: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := d.Round(c); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("the webhook received %d events, want 1", len(received))
	}
	if got := received[0]["type"]; got != calendarv1svc.EventCreatedEvent {
		t.Errorf("type %v, want %s", got, calendarv1svc.EventCreatedEvent)
	}
	if got := received[0]["subject"]; got != "calendars/rooms/events/standup" {
		t.Errorf("subject %v", got)
	}

	// The store's own contract, beyond what one round touched.
	cur := must(store.GetCursor(c, "example.acme.calendar.event"))
	if err := store.UpdateCursor(c, events.Cursor{Feed: cur.Feed, Position: time.Now(), Etag: "stale"}); !errors.Is(err, events.ErrConflict) {
		t.Errorf("a stale cursor etag: %v, want events.ErrConflict", err)
	}
	if _, err := store.CreateCursor(c, cur); !errors.Is(err, events.ErrAlreadyExists) {
		t.Errorf("a second cursor: %v, want events.ErrAlreadyExists", err)
	}
	if _, err := store.GetWebhook(c, "webhooks/none"); !errors.Is(err, events.ErrNotFound) {
		t.Errorf("a missing webhook: %v, want events.ErrNotFound", err)
	}
	if err := store.PruneDeliveries(c, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	if due := must(store.DueDeliveries(c, time.Now().Add(time.Hour), 10)); len(due) != 0 {
		t.Errorf("%d deliveries still due after the endpoint accepted", len(due))
	}
}

// TestIdempotency runs runtime-go's idempotency interceptor over the
// generated idempotency.Store: a request_id replayed returns the first
// response without running the handler again.
func TestIdempotency(t *testing.T) {
	conn, c := open(t), ctx(t)
	store := &idempotencyv1svc.IdempotencyStore{Keys: requestkeys.New(conn).IdempotencyKeys}
	intercept := idempotency.Interceptor(store, idempotency.Options{Domain: "booking.acme.example"})

	runs := 0
	handler := func(context.Context, any) (any, error) {
		runs++
		return wrapperspb.String("first"), nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/acme.booking.v1.Reservations/CreateReservation"}
	// Any request declaring a string request_id opts in; the key message
	// itself has one.
	withID := func(id string) any { return &idempotencyv1.IdempotencyKey{RequestId: id} }

	first := must(intercept(c, withID("7f9c2a52-1b9e-4d3f-9a8e-0c1d2e3f4a5b"), info, handler))
	again := must(intercept(c, withID("7f9c2a52-1b9e-4d3f-9a8e-0c1d2e3f4a5b"), info, handler))
	if runs != 1 {
		t.Fatalf("the handler ran %d times for one request_id", runs)
	}
	if first.(*wrapperspb.StringValue).GetValue() != again.(*wrapperspb.StringValue).GetValue() {
		t.Fatalf("the replay returned %v, want %v", again, first)
	}
}

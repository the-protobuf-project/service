// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package spec

// extras.go finds what a file declares beyond its services: resources opted
// into the change feed, enums of error reasons, and the records runtime-go's
// stores keep.

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/pb/servicepbv1"
	"github.com/the-protobuf-project/service/plugin/servicev1"
)

func (b *builder) extras(f *protogen.File, pkgFor func(*protogen.File) *Package) error {
	var walk func(ms []*protogen.Message) error
	walk = func(ms []*protogen.Message) error {
		for _, m := range ms {
			if servicev1.Event(m.Desc) != nil {
				r, why := b.resource(m)
				switch {
				case r == nil:
					b.warn("%s declares (service.v1.event) but has no change feed: %s", m.Desc.FullName(), why)
				case r.Events == nil || r.Events.Feed == nil:
					b.warn("%s declares (service.v1.event) but has no change feed: a feed reads update_time and etag, and it lacks one", m.Desc.FullName())
				default:
					pkg := pkgFor(f)
					pkg.Feeds = append(pkg.Feeds, r)
				}
			}
			if ao := adapterOf(m); ao != nil && ao.GetRole() != servicepbv1.AdapterOptions_ROLE_UNSPECIFIED {
				a, err := b.adapter(m, ao)
				if err != nil {
					return err
				}
				pkg := pkgFor(f)
				pkg.Adapters = append(pkg.Adapters, a)
			}
			if err := walk(m.Messages); err != nil {
				return err
			}
			for _, e := range m.Enums {
				if err := b.errorEnum(f, e, pkgFor); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(f.Messages); err != nil {
		return err
	}
	for _, e := range f.Enums {
		if err := b.errorEnum(f, e, pkgFor); err != nil {
			return err
		}
	}
	return nil
}

func adapterOf(m *protogen.Message) *servicepbv1.AdapterOptions {
	if !proto.HasExtension(m.Desc.Options(), servicepbv1.E_Adapter) {
		return nil
	}
	o, _ := proto.GetExtension(m.Desc.Options(), servicepbv1.E_Adapter).(*servicepbv1.AdapterOptions)
	return o
}

// rpcCodes maps google.rpc.Code names to grpc-go's codes constants.
var rpcCodes = map[string]string{
	"CANCELLED": "Canceled", "UNKNOWN": "Unknown", "INVALID_ARGUMENT": "InvalidArgument",
	"DEADLINE_EXCEEDED": "DeadlineExceeded", "NOT_FOUND": "NotFound", "ALREADY_EXISTS": "AlreadyExists",
	"PERMISSION_DENIED": "PermissionDenied", "RESOURCE_EXHAUSTED": "ResourceExhausted",
	"FAILED_PRECONDITION": "FailedPrecondition", "ABORTED": "Aborted", "OUT_OF_RANGE": "OutOfRange",
	"UNIMPLEMENTED": "Unimplemented", "INTERNAL": "Internal", "UNAVAILABLE": "Unavailable",
	"DATA_LOSS": "DataLoss", "UNAUTHENTICATED": "Unauthenticated",
}

var details = map[servicepbv1.ErrorReasonOptions_DetailKind]string{
	servicepbv1.ErrorReasonOptions_DETAIL_KIND_BAD_REQUEST:          "BadRequest",
	servicepbv1.ErrorReasonOptions_DETAIL_KIND_RESOURCE_INFO:        "ResourceInfo",
	servicepbv1.ErrorReasonOptions_DETAIL_KIND_PRECONDITION_FAILURE: "PreconditionFailure",
}

func (b *builder) errorEnum(f *protogen.File, e *protogen.Enum, pkgFor func(*protogen.File) *Package) error {
	if !proto.HasExtension(e.Desc.Options(), servicepbv1.E_ErrorDomain) {
		return nil
	}
	do, _ := proto.GetExtension(e.Desc.Options(), servicepbv1.E_ErrorDomain).(*servicepbv1.ErrorDomainOptions)
	if do.GetDomain() == "" {
		return fmt.Errorf("service: %s: (service.v1.error_domain) needs a domain, the ErrorInfo domain its reasons are returned in (AIP-193), e.g. \"example.com\"", e.Desc.FullName())
	}
	ee := &ErrorEnum{Enum: e, Domain: do.GetDomain()}
	for _, v := range e.Values {
		if v.Desc.Number() == 0 {
			continue
		}
		ro, _ := proto.GetExtension(v.Desc.Options(), servicepbv1.E_ErrorReason).(*servicepbv1.ErrorReasonOptions)
		if ro == nil {
			return fmt.Errorf("service: %s: every reason of an error_domain enum needs (service.v1.error_reason) with at least a code", v.Desc.FullName())
		}
		code, ok := rpcCodes[ro.GetCode()]
		if !ok {
			return fmt.Errorf("service: %s: code %q is not a google.rpc.Code name (OK is not an error); use one such as \"FAILED_PRECONDITION\"", v.Desc.FullName(), ro.GetCode())
		}
		name := valueSuffix(e, v)
		ee.Reasons = append(ee.Reasons, &ErrorReason{
			Name: name, GoName: naming.PascalGo(strings.ToLower(name)), Code: code, CodeName: ro.GetCode(),
			Description: ro.GetDescription(), Fix: ro.GetFix(), Detail: details[ro.GetDetail()],
			Retryable: ro.GetRetryable(), Sentinel: ro.GetSentinel(),
		})
	}
	pkg := pkgFor(f)
	if len(pkg.Errors) > 0 && pkg.Errors[0].Domain != ee.Domain {
		return fmt.Errorf("service: %s: package %s declares error reasons in two domains (%s and %s); one package's NewTranslator speaks one domain, so move one enum to another package or give both the same domain",
			e.Desc.FullName(), f.Desc.Package(), pkg.Errors[0].Domain, ee.Domain)
	}
	pkg.Errors = append(pkg.Errors, ee)
	return nil
}

// roles are each adapter role's conventional fields, its required ones, and
// the state values it reads.
var roles = map[servicepbv1.AdapterOptions_Role]struct {
	name     string
	fields   []string
	required []string
	states   []string
}{
	servicepbv1.AdapterOptions_ROLE_WEBHOOK: {"Webhook",
		[]string{"uri", "event_types", "scope", "paused", "state", "secret", "previous_secret", "rotate_time", "create_time", "delete_time"},
		[]string{"uri"}, []string{"PAUSED"}},
	servicepbv1.AdapterOptions_ROLE_DELIVERY: {"Delivery",
		[]string{"event_id", "event_type", "subject", "payload", "state", "attempt_count", "response_code", "error_message", "next_attempt_time", "deliver_time", "create_time", "update_time", "etag"},
		[]string{"event_id", "event_type", "subject", "payload", "state", "attempt_count", "next_attempt_time", "update_time", "etag"},
		[]string{"PENDING", "SUCCEEDED", "FAILED"}},
	servicepbv1.AdapterOptions_ROLE_FEED_CURSOR: {"FeedCursor",
		[]string{"position", "etag"}, []string{"position", "etag"}, nil},
	servicepbv1.AdapterOptions_ROLE_IDEMPOTENCY_KEY: {"IdempotencyKey",
		[]string{"method", "request_id", "state", "response", "create_time"},
		[]string{"method", "request_id", "state", "response"}, []string{"IN_FLIGHT", "DONE"}},
}

func (b *builder) adapter(m *protogen.Message, ao *servicepbv1.AdapterOptions) (*Adapter, error) {
	role := roles[ao.GetRole()]
	r, why := b.resource(m)
	if r == nil {
		return nil, fmt.Errorf("service: %s: (service.v1.adapter) role %s needs a generated repository: %s", m.Desc.FullName(), ao.GetRole(), why)
	}
	a := &Adapter{Role: role.name, Resource: r, Fields: map[string]*protogen.Field{}, States: map[string]*protogen.EnumValue{}}
	for k := range ao.GetFields() {
		if !slices.Contains(role.fields, k) {
			return nil, fmt.Errorf("service: %s: (service.v1.adapter).fields names %q, which is not a field of role %s (%s)", m.Desc.FullName(), k, ao.GetRole(), strings.Join(role.fields, ", "))
		}
	}
	for _, conv := range role.fields {
		name := conv
		if o := ao.GetFields()[conv]; o != "" {
			name = o
		}
		if fd := fieldNamed(m, name); fd != nil {
			a.Fields[conv] = fd
		} else if ao.GetFields()[conv] != "" {
			return nil, fmt.Errorf("service: %s: (service.v1.adapter).fields maps %s to %q, which the message does not have", m.Desc.FullName(), conv, name)
		}
	}
	for _, req := range role.required {
		if a.Fields[req] == nil {
			return nil, fmt.Errorf("service: %s: role %s needs a %s field (or a fields entry naming the one that holds it)", m.Desc.FullName(), ao.GetRole(), req)
		}
	}
	if st := a.Fields["state"]; st != nil && st.Enum != nil {
		for _, v := range st.Enum.Values {
			for _, want := range role.states {
				if strings.HasSuffix(string(v.Desc.Name()), "_"+want) || string(v.Desc.Name()) == want {
					a.States[want] = v
				}
			}
		}
		for _, want := range role.states {
			if a.States[want] == nil && role.name != "Webhook" {
				return nil, fmt.Errorf("service: %s: role %s reads state %s, and %s has no value ending in _%s", m.Desc.FullName(), ao.GetRole(), want, st.Enum.Desc.FullName(), want)
			}
		}
	}
	if role.name == "Delivery" && len(r.Segments) != 2 {
		return nil, fmt.Errorf("service: %s: a delivery is named under its webhook (webhooks/{webhook}/deliveries/{delivery}); its pattern is %q", m.Desc.FullName(), r.Pattern)
	}
	return a, nil
}

// feedMask is the read mask a change read of r reads, or nil when r cannot
// be a feed.
func feedMask(m *protogen.Message, e *Events) []string {
	if fieldNamed(m, "update_time") == nil || !isString(fieldNamed(m, "etag")) {
		return nil
	}
	mask := []string{"name", "etag", "update_time"}
	for _, f := range []string{"create_time", "delete_time"} {
		if fieldNamed(m, f) != nil {
			mask = append(mask, f)
		}
	}
	if e.State != nil {
		mask = append(mask, string(e.State.Desc.Name()))
	}
	return mask
}

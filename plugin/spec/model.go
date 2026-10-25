// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package spec decides what protoc-gen-service generates. It classifies every
// method of every service as one of the AIP standard methods it can serve over
// a generated repository, or as custom; it resolves each resource to the
// repository protoc-gen-store wrote for it; and it collects the event
// declarations. Templates render what this package decided and decide nothing
// themselves.
package spec

import (
	"google.golang.org/protobuf/compiler/protogen"
)

// Kind is the AIP standard method a method is, or Custom.
type Kind int

// The method kinds.
const (
	Custom   Kind = iota
	Get           // AIP-131
	List          // AIP-132
	Create        // AIP-133
	Update        // AIP-134
	Delete        // AIP-135
	Undelete      // AIP-164
	Purge         // AIP-164 (single-resource purge)
)

var kindNames = map[Kind]string{
	Custom: "Custom", Get: "Get", List: "List", Create: "Create",
	Update: "Update", Delete: "Delete", Undelete: "Undelete", Purge: "Purge",
}

func (k Kind) String() string { return kindNames[k] }

// Model is everything one plugin run generates.
type Model struct {
	Packages []*Package
	// Warnings are methods that look standard but could not be generated,
	// with the reason; the plugin prints them and leaves those methods to
	// the consumer.
	Warnings []string
}

// Package is one proto package: one generated Go package of servers.
type Package struct {
	// Feeds are the resources of the package opted into the change feed;
	// Errors its error_domain enums; Adapters its store records.
	Feeds    []*Resource
	Errors   []*ErrorEnum
	Adapters []*Adapter

	ProtoPackage string // "protobuf.rfc4519.organization.v1"
	// Dir is the output directory, relative to the plugin's out:
	// the proto package as a path plus the Go package name, like
	// protoc-gen-mcp's package_suffix layout.
	Dir       string
	GoPackage string // "organizationv1svc"
	Services  []*Service
}

// Service is one generated server.
type Service struct {
	Proto  *protogen.Service
	GoName string // "Organizations"
	// FullName is the gRPC service name ("protobuf.rfc4519.organization.v1.Organizations").
	FullName string
	// Grpc, Gateway and MCP are where the service's bindings live.
	Grpc    GoPkg
	Gateway *GoPkg // nil when the service has no HTTP rules or gateway=false
	MCP     *GoPkg // nil when mcp=false
	// Domain is the default google.rpc.ErrorInfo domain.
	Domain string
	// EventSource is the default CloudEvents source.
	EventSource string

	Resources []*Resource
	Methods   []*Method
}

// Standard reports whether s generates any standard method.
func (s *Service) Standard() bool {
	for _, m := range s.Methods {
		if m.Kind != Custom {
			return true
		}
	}
	return false
}

// Has reports whether s serves kind k for r.
func (s *Service) Has(r *Resource, k Kind) bool {
	for _, m := range s.Methods {
		if m.Kind == k && m.Resource == r {
			return true
		}
	}
	return false
}

// GoPkg is a Go package the generated code imports.
type GoPkg struct {
	Path string
	Name string
}

// Resource is one AIP resource a service serves over a generated repository.
type Resource struct {
	Message *protogen.Message
	Type    string // "the-protobuf-project.org/Organization"
	Pattern string // "organizations/{organization}"
	// Segments are the pattern's collection/{var} pairs.
	Segments []Segment
	Singular string // "organization", "organizational_unit"
	// Repository is the store's generated package for the resource's schema,
	// RepoInterface its interface ("OrganizationRepository").
	Repository    GoPkg
	RepoInterface string
	// Parented is the store's rule: a pattern of more than one segment, whose
	// Create and List take a parent.
	Parented   bool
	SoftDelete bool // AIP-164 delete_time: Undelete exists
	HasEtag    bool

	// Field sets by google.api.field_behavior, by proto name.
	Required   []string
	OutputOnly []string
	Immutable  []string

	Events *Events // nil when the resource declares none
}

// Collections are r's pattern collections, in order.
func (r *Resource) Collections() []string {
	out := make([]string, len(r.Segments))
	for i, s := range r.Segments {
		out[i] = s.Collection
	}
	return out
}

// ParentCollections are the collections of r's parent pattern.
func (r *Resource) ParentCollections() []string {
	c := r.Collections()
	if len(c) == 0 {
		return nil
	}
	return c[:len(c)-1]
}

// Collection is r's own collection id ("events").
func (r *Resource) Collection() string { return r.Segments[len(r.Segments)-1].Collection }

// Segment is one collection/{var} pair of a resource pattern.
type Segment struct {
	Collection string
	Var        string
}

// Events are a resource's event declarations.
type Events struct {
	Prefix   string // "freebusy.reservation.v1.reservation"
	ID       string // the feed's cursor id
	ScopeVar string // (service.v1.event).scope
	// ScopeCollection is the collection of ScopeVar's segment, "" for none.
	ScopeCollection string
	// Feed is set when the resource can be read as a change feed: it has an
	// update_time and an etag, and Feed is its read mask.
	Feed      []string
	Lifecycle []Lifecycle // the lifecycle events not omitted
	// State is the enum field the state events read, and States the values
	// that declare one. Both empty when no value of the resource's state enum
	// declares an event.
	State  *protogen.Field
	States []StateEvent
}

// Lifecycle is one lifecycle event.
type Lifecycle struct {
	Suffix string // "created"
	GoName string // "Created"
	Type   string // "<prefix>.created"
}

// StateEvent is the event a record entering one state emits.
type StateEvent struct {
	Value *protogen.EnumValue
	Type  string
	// GoName is the value's suffix after the enum prefix, Pascal-cased
	// ("Confirmed" for RESERVATION_STATE_CONFIRMED).
	GoName string
}

// Method is one method of a service.
type Method struct {
	Proto    *protogen.Method
	Kind     Kind
	Resource *Resource // nil for Custom

	// The request fields a standard method reads; nil when absent.
	Name, Parent, Body, ID, UpdateMask, Etag *protogen.Field
	AllowMissing, ValidateOnly, Force        *protogen.Field
	PageSize, PageToken, Filter, OrderBy     *protogen.Field
	ShowDeleted, ReadMask                    *protogen.Field

	// List's response fields.
	Items, NextPageToken *protogen.Field

	// ReturnsEmpty is Delete or Purge answering google.protobuf.Empty rather
	// than the resource.
	ReturnsEmpty bool
}

// ErrorEnum is an enum of error reasons (service.v1.error_domain).
type ErrorEnum struct {
	Enum    *protogen.Enum
	Domain  string
	Reasons []*ErrorReason
}

// ErrorReason is one value of an ErrorEnum.
type ErrorReason struct {
	Name        string // "HOLD_NOT_ACTIVE": the value without the enum prefix
	GoName      string // "HoldNotActive"
	Code        string // the grpc codes constant, "FailedPrecondition"
	CodeName    string // "FAILED_PRECONDITION"
	Description string
	Fix         string
	Detail      string // "BadRequest", "ResourceInfo", "PreconditionFailure", ""
	Retryable   bool
	Sentinel    bool
}

// Adapter is a resource a runtime-go store keeps (service.v1.adapter).
type Adapter struct {
	Role     string // "Webhook", "Delivery", "FeedCursor", "IdempotencyKey"
	Resource *Resource
	// Fields maps each conventional field to the resource's field; a field
	// the resource lacks is absent.
	Fields map[string]*protogen.Field
	// Enum values a state field maps from, by conventional suffix
	// ("PENDING", "PAUSED", "IN_FLIGHT").
	States map[string]*protogen.EnumValue
}

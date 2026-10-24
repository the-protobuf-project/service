// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package servicev1 reads the service.v1 vocabulary off descriptors.
//
// It is a protokit FacetReader so the plugin composes like every other
// protokit plugin (provides: service), but almost nothing service.v1 says is
// about a table: services and methods are not part of the neutral IR at all.
// So the reader contributes the one message-level facet (event) to the IR and
// the accessors below read the rest straight off the descriptors the
// generator walks.
//
// Like cache.v1, this vocabulary cannot move a name. The reader implements no
// StructureReader, so the database, schema and table a resource lands in are
// exactly the ones protoc-gen-store derived -- which is what lets the
// generated servers name the store's repository packages without being told.
package servicev1

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/entity"

	"github.com/the-protobuf-project/service/plugin/pb/servicepbv1"
)

// Key is the facet key the reader registers under.
const Key = "service.v1"

// Reader returns the service.v1 FacetReader.
func Reader() protokit.FacetReader { return reader{} }

// Readers are the readers this plugin builds its IR with: entity.v1, through
// the store's own reader so the names agree, and service.v1.
func Readers() []protokit.FacetReader {
	return []protokit.FacetReader{entity.Reader(), Reader()}
}

type reader struct{}

var _ schema.FacetReader = reader{}

func (reader) Key() string { return Key }

func (reader) ReadFile(protoreflect.FileDescriptor) (any, error) { return nil, nil }

// ReadMessage contributes a resource's (service.v1.event), when it has one.
func (reader) ReadMessage(d protoreflect.MessageDescriptor) (any, error) {
	if e := Event(d); e != nil {
		return e, nil
	}
	return nil, nil
}

func (reader) ReadField(protoreflect.FieldDescriptor) (any, error) { return nil, nil }

// Service returns a service's (service.v1.service), or nil.
func Service(d protoreflect.ServiceDescriptor) *servicepbv1.ServiceOptions {
	if d == nil || !proto.HasExtension(d.Options(), servicepbv1.E_Service) {
		return nil
	}
	o, _ := proto.GetExtension(d.Options(), servicepbv1.E_Service).(*servicepbv1.ServiceOptions)
	return o
}

// Method returns a method's (service.v1.method), or nil.
func Method(d protoreflect.MethodDescriptor) *servicepbv1.MethodOptions {
	if d == nil || !proto.HasExtension(d.Options(), servicepbv1.E_Method) {
		return nil
	}
	o, _ := proto.GetExtension(d.Options(), servicepbv1.E_Method).(*servicepbv1.MethodOptions)
	return o
}

// Event returns a resource message's (service.v1.event), or nil.
func Event(d protoreflect.MessageDescriptor) *servicepbv1.EventOptions {
	if d == nil || !proto.HasExtension(d.Options(), servicepbv1.E_Event) {
		return nil
	}
	o, _ := proto.GetExtension(d.Options(), servicepbv1.E_Event).(*servicepbv1.EventOptions)
	return o
}

// StateEvent returns an enum value's (service.v1.state_event), or nil.
func StateEvent(d protoreflect.EnumValueDescriptor) *servicepbv1.EventOptions {
	if d == nil || !proto.HasExtension(d.Options(), servicepbv1.E_StateEvent) {
		return nil
	}
	o, _ := proto.GetExtension(d.Options(), servicepbv1.E_StateEvent).(*servicepbv1.EventOptions)
	return o
}

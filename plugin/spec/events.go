// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/servicev1"
)

// lifecycle is every lifecycle event, in the order a record meets them.
var lifecycle = []string{"created", "updated", "deleted"}

// events reads m's event declarations: (service.v1.event) on the message for
// its lifecycle events, and (service.v1.state_event) on the values of an enum
// field of m for its state events. A state event needs no message option;
// the prefix then defaults the same way.
func events(m *protogen.Message) *Events {
	opt := servicev1.Event(m.Desc)
	prefix := strings.ToLower(string(m.Desc.FullName()))
	if opt.GetType() != "" {
		prefix = opt.GetType()
	}
	e := &Events{Prefix: prefix, ID: prefix, ScopeVar: opt.GetScope()}
	if opt != nil {
		for _, s := range lifecycle {
			if slices.Contains(opt.GetOmit(), s) {
				continue
			}
			e.Lifecycle = append(e.Lifecycle, Lifecycle{Suffix: s, GoName: naming.PascalGo(s), Type: prefix + "." + s})
		}
	}
	for _, f := range m.Fields {
		if f.Enum == nil || f.Desc.IsList() {
			continue
		}
		var states []StateEvent
		for _, v := range f.Enum.Values {
			so := servicev1.StateEvent(v.Desc)
			if so == nil {
				continue
			}
			suffix := valueSuffix(f.Enum, v)
			t := so.GetType()
			if t == "" {
				t = prefix + "." + strings.ToLower(suffix)
			}
			states = append(states, StateEvent{Value: v, Type: t, GoName: naming.PascalGo(strings.ToLower(suffix))})
		}
		if len(states) > 0 && e.State == nil {
			e.State, e.States = f, states
		}
	}
	if len(e.Lifecycle) == 0 && e.State == nil {
		return nil
	}
	return e
}

// valueSuffix is v's name without its enum's prefix:
// RESERVATION_STATE_CONFIRMED in ReservationState is CONFIRMED.
func valueSuffix(e *protogen.Enum, v *protogen.EnumValue) string {
	prefix := strings.ToUpper(naming.SnakeCase(string(e.Desc.Name()))) + "_"
	return strings.TrimPrefix(string(v.Desc.Name()), prefix)
}

// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/service/plugin/spec"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
)

func (t *Target) errors(b *strings.Builder, p *spec.Package) {
	if len(p.Errors) == 0 {
		return
	}
	fmt.Fprintf(b, "pub const ERROR_DOMAIN: &str = %s;\n", q(p.Errors[0].Domain))
	var retry []string
	for _, e := range p.Errors {
		for _, r := range e.Reasons {
			code := r.Code
			if code == "Canceled" {
				code = "Cancelled"
			}
			fmt.Fprintf(b, "pub const REASON_%s: &str = %s;\n", r.Name, q(r.Name))
			params := "what: impl Into<String>"
			field, resType, resName := `""`, `""`, `""`
			switch r.Detail {
			case "BadRequest":
				params = "field: &str, " + params
				field = "field"
			case "ResourceInfo", "PreconditionFailure":
				params = "resource_type: &str, name: &str, " + params
				resType = "resource_type"
				resName = "name"
			}
			fmt.Fprintf(b, "pub fn new_%s(%s) -> tonic::Status { servicex::detailed_reason(tonic::Code::%s,ERROR_DOMAIN,%s,what.into(),servicex::ReasonDetail {fix:%s,field:%s,resource_type:%s,name:%s}) }\n", ident(r.GoName), params, code, q(r.Name), q(r.Fix), field, resType, resName)
			if r.Retryable {
				retry = append(retry, q(r.Name))
			}
		}
	}
	fmt.Fprintf(b, "pub fn retryable(reason:&str)->bool { %s.contains(&reason) }\n", "["+strings.Join(retry, ",")+"]")
}
func (t *Target) feeds(b *strings.Builder, p *spec.Package) {
	for _, r := range p.Feeds {
		e := r.Events
		name := ident(r.Message.GoIdent.GoName)
		lifecycle := []string{}
		states := []string{}
		for _, l := range e.Lifecycle {
			fmt.Fprintf(b, "pub const %s_%s_EVENT: &str = %s;\n", strings.ToUpper(name), strings.ToUpper(l.Suffix), q(l.Type))
			lifecycle = append(lifecycle, "("+q(l.Suffix)+","+q(l.Type)+")")
		}
		for _, st := range e.States {
			fmt.Fprintf(b, "pub const %s_%s_EVENT: &str = %s;\n", strings.ToUpper(name), strings.ToUpper(naming.SnakeCase(st.GoName)), q(st.Type))
			states = append(states, fmt.Sprintf("(%d,%s)", st.Value.Desc.Number(), q(st.Type)))
		}
		scope := "None"
		for i, seg := range r.Segments {
			if seg.Var == e.ScopeVar {
				scope = fmt.Sprintf("Some(%d)", i)
			}
		}
		parent := []string{}
		for _, c := range r.ParentCollections() {
			parent = append(parent, c, "-")
		}
		state := ""
		if e.State != nil {
			state = string(e.State.Desc.Name())
		}
		fmt.Fprintf(b, "pub fn %s_feed(repository: std::sync::Arc<dyn servicex::Repository<%s>>, options: servicex::Options) -> servicex::Feed<%s> { servicex::Feed { repository, options, spec: servicex::FeedSpec { id:%s, message:%s, parent:%s, read_mask:%s, lifecycle:&[%s], state_field:%s, states:&[%s], scope:%s } } }\n", name, t.typ(r.Message), t.typ(r.Message), q(e.ID), q(string(r.Message.Desc.FullName())), q(strings.Join(parent, "/")), arr(e.Feed), strings.Join(lifecycle, ","), q(state), strings.Join(states, ","), scope)
	}
	if len(p.Feeds) > 0 {
		b.WriteString("pub fn event_types() -> &'static [&'static str] { &[")
		for _, r := range p.Feeds {
			for _, l := range r.Events.Lifecycle {
				fmt.Fprintf(b, "%s,", q(l.Type))
			}
			for _, st := range r.Events.States {
				fmt.Fprintf(b, "%s,", q(st.Type))
			}
		}
		b.WriteString("] }\n")
	}
}
func (t *Target) adapters(b *strings.Builder, p *spec.Package) error {
	roles := map[string]*spec.Adapter{}
	for _, a := range p.Adapters {
		if roles[a.Role] != nil {
			return fmt.Errorf("service: duplicate Rust adapter role %s in %s", a.Role, p.ProtoPackage)
		}
		roles[a.Role] = a
		keys := []string{}
		for k := range a.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(b, "const %s_SPEC: servicex::AdapterSpec = servicex::AdapterSpec { message:%s, collection:%s, parent:%s, fields:&[", strings.ToUpper(naming.SnakeCase(a.Role)), q(string(a.Resource.Message.Desc.FullName())), q(a.Resource.Collection()), q(func() string {
			if a.Resource.Parented {
				return a.Resource.Segments[0].Collection + "/-"
			}
			return ""
		}()))
		for _, k := range keys {
			fmt.Fprintf(b, "(%s,%s,%t),", q(k), q(string(a.Fields[k].Desc.Name())), outputOnly(a.Fields[k]))
		}
		b.WriteString("], states:&[")
		keys = nil
		for k := range a.States {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, "(%s,%d,%s),", q(k), a.States[k].Desc.Number(), q(string(a.States[k].Desc.Name())))
		}
		b.WriteString("] };\n")
	}
	if a := roles["IdempotencyKey"]; a != nil {
		if a.Resource.Parented {
			return fmt.Errorf("service: Rust idempotency keys must be top-level resources")
		}
		typ := t.typ(a.Resource.Message)
		fmt.Fprintf(b, "pub type IdempotencyStore = servicex::IdempotencyStore<%s>;\npub fn idempotency_store(keys:std::sync::Arc<dyn servicex::Repository<%s>>,options:servicex::Options)->IdempotencyStore {servicex::IdempotencyStore{keys,options,spec:IDEMPOTENCY_KEY_SPEC}}\n", typ, typ)
	}
	w, d, c := roles["Webhook"], roles["Delivery"], roles["FeedCursor"]
	if w != nil || d != nil || c != nil {
		if w == nil || d == nil || c == nil {
			return fmt.Errorf("service: Rust event adapter requires webhook, delivery and feed cursor in one package")
		}
		if w.Resource.Parented || c.Resource.Parented {
			return fmt.Errorf("service: Rust webhook and cursor adapters require top-level resources")
		}
		wt, dt, ct := t.typ(w.Resource.Message), t.typ(d.Resource.Message), t.typ(c.Resource.Message)
		fmt.Fprintf(b, "pub type EventStore = servicex::EventStore<%s,%s,%s>;\npub fn event_store(webhooks:std::sync::Arc<dyn servicex::Repository<%s>>,deliveries:std::sync::Arc<dyn servicex::Repository<%s>>,cursors:std::sync::Arc<dyn servicex::Repository<%s>>,options:servicex::Options)->EventStore {servicex::EventStore{webhooks,deliveries,cursors,options,webhook_spec:WEBHOOK_SPEC,delivery_spec:DELIVERY_SPEC,cursor_spec:FEED_CURSOR_SPEC}}\n", wt, dt, ct, wt, dt, ct)
	}
	return nil
}
func outputOnly(f *protogen.Field) bool {
	bs, _ := proto.GetExtension(f.Desc.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, v := range bs {
		if v == annotations.FieldBehavior_OUTPUT_ONLY {
			return true
		}
	}
	return false
}

// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/the-protobuf-project/service/plugin/servicev1"
)

const (
	emptyName     = "google.protobuf.Empty"
	fieldMaskName = "google.protobuf.FieldMask"
)

// method classifies m. A method is standard only when its name, request and
// response all have the AIP shape and its resource has a generated
// repository; anything else is Custom, and the consumer writes it.
func (b *builder) method(svc *protogen.Service, m *protogen.Method) *Method {
	out := &Method{Proto: m}
	if mo := servicev1.Method(m.Desc); mo.GetCustom() {
		return out
	}
	if m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer() {
		return out
	}
	name := m.GoName
	in, resp := m.Input, m.Output
	field := func(n string) *protogen.Field { return fieldNamed(in, n) }

	var kind Kind
	var res *protogen.Message
	switch {
	case strings.HasPrefix(name, "Get"):
		kind, res = Get, resp
		if name != "Get"+string(resp.Desc.Name()) || !isString(field("name")) {
			return out
		}
	case strings.HasPrefix(name, "List"):
		items, next := listFields(resp)
		if items == nil || next == nil {
			return out
		}
		kind, res = List, items.Message
		out.Items, out.NextPageToken = items, next
	case strings.HasPrefix(name, "Create"):
		kind, res = Create, resp
		if name != "Create"+string(resp.Desc.Name()) {
			return out
		}
	case strings.HasPrefix(name, "Update"):
		kind, res = Update, resp
		if name != "Update"+string(resp.Desc.Name()) {
			return out
		}
	case strings.HasPrefix(name, "Undelete"):
		kind, res = Undelete, resp
		if name != "Undelete"+string(resp.Desc.Name()) || !isString(field("name")) {
			return out
		}
	case strings.HasPrefix(name, "Delete"), strings.HasPrefix(name, "Purge"):
		kind = Delete
		suffix := strings.TrimPrefix(name, "Delete")
		if strings.HasPrefix(name, "Purge") {
			kind, suffix = Purge, strings.TrimPrefix(name, "Purge")
		}
		if !isString(field("name")) {
			return out
		}
		switch resp.Desc.FullName() {
		case emptyName:
			out.ReturnsEmpty = true
			res = b.referenced(field("name"), suffix)
		default:
			res = resp
		}
		if res == nil || string(res.Desc.Name()) != suffix {
			return out
		}
		if kind == Purge && !out.ReturnsEmpty {
			return out
		}
	default:
		return out
	}

	r, why := b.resource(res)
	if r == nil {
		b.warn("%s.%s looks like a standard %s method but stays custom: %s", svc.Desc.FullName(), name, kind, why)
		return out
	}
	if kind == Undelete && !r.SoftDelete {
		b.warn("%s.%s stays custom: %s has no OUTPUT_ONLY delete_time, so its repository has no Undelete", svc.Desc.FullName(), name, r.Message.Desc.FullName())
		return out
	}

	out.Name = stringField(in, "name")
	out.Etag = stringField(in, "etag")
	out.ValidateOnly = boolField(in, "validate_only")
	out.AllowMissing = boolField(in, "allow_missing")
	out.Force = boolField(in, "force")

	switch kind {
	case List, Create:
		if r.Parented {
			out.Parent = stringField(in, "parent")
			if out.Parent == nil {
				b.warn("%s.%s stays custom: %s is nested (%s) and the request has no parent", svc.Desc.FullName(), name, r.Message.Desc.FullName(), r.Pattern)
				return &Method{Proto: m}
			}
		}
	}
	switch kind {
	case List:
		out.PageSize = int32Field(in, "page_size")
		out.PageToken = stringField(in, "page_token")
		out.Filter = stringField(in, "filter")
		out.OrderBy = stringField(in, "order_by")
		out.ShowDeleted = boolField(in, "show_deleted")
		if f := field("read_mask"); isMessage(f, fieldMaskName) {
			out.ReadMask = f
		}
	case Create, Update:
		out.Body = bodyField(in, res)
		if out.Body == nil {
			return &Method{Proto: m}
		}
		if kind == Create {
			out.ID = stringField(in, r.Singular+"_id")
		} else if f := field("update_mask"); isMessage(f, fieldMaskName) {
			out.UpdateMask = f
		}
	}
	out.Kind, out.Resource = kind, r
	return out
}

// referenced finds the resource a name field refers to, by its
// google.api.resource_reference, or by the method suffix.
func (b *builder) referenced(f *protogen.Field, suffix string) *protogen.Message {
	if rr, _ := proto.GetExtension(f.Desc.Options(), annotations.E_ResourceReference).(*annotations.ResourceReference); rr != nil && rr.GetType() != "" {
		for _, m := range b.byMsg {
			rd, _ := proto.GetExtension(m.Desc.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
			if rd != nil && rd.GetType() == rr.GetType() {
				return m
			}
		}
	}
	// Fall back to a message of the suffix's name in the method's own package
	// or, failing that, anywhere in the request.
	var found *protogen.Message
	for full, m := range b.byMsg {
		if string(m.Desc.Name()) == suffix && proto.HasExtension(m.Desc.Options(), annotations.E_Resource) {
			if found == nil || full.Parent() == f.Desc.ContainingMessage().FullName().Parent() {
				found = m
			}
		}
	}
	return found
}

// listFields returns a List response's repeated resource field and its
// next_page_token.
func listFields(resp *protogen.Message) (items, next *protogen.Field) {
	for _, f := range resp.Fields {
		switch {
		case f.Desc.IsList() && f.Message != nil && items == nil &&
			proto.HasExtension(f.Message.Desc.Options(), annotations.E_Resource):
			items = f
		case string(f.Desc.Name()) == "next_page_token" && isString(f):
			next = f
		}
	}
	return items, next
}

func bodyField(in, res *protogen.Message) *protogen.Field {
	for _, f := range in.Fields {
		if isMessage(f, res.Desc.FullName()) {
			return f
		}
	}
	return nil
}

func stringField(m *protogen.Message, name string) *protogen.Field {
	if f := fieldNamed(m, name); isString(f) {
		return f
	}
	return nil
}

func boolField(m *protogen.Message, name string) *protogen.Field {
	if f := fieldNamed(m, name); isBool(f) {
		return f
	}
	return nil
}

func int32Field(m *protogen.Message, name string) *protogen.Field {
	if f := fieldNamed(m, name); isInt32(f) {
		return f
	}
	return nil
}

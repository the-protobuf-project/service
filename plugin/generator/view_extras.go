// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

// view_extras.go prepares the per-package files beyond the servers: the
// change feeds, the error reasons and the runtime-go store adapters.

import (
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/the-protobuf-project/service/plugin/spec"
)

type feedsFile struct {
	Banner  string
	Package string
	Imports []string
	Feeds   []feedView
}

type feedView struct {
	GoName     string
	PB         string
	Repo       string
	ID         string
	Mask       string // Go literal
	Wildcard   string // List's parent, "" for a top-level resource
	Lifecycle  []spec.Lifecycle
	States     []typesState
	StateEnum  string
	StateGet   string
	CreateGet  string // "GetCreateTime()" or ""
	DeleteGet  string
	Scope      string // collection the scope is cut at
	HasCreated bool
}

type typesState struct {
	Const, Value, Type string
}

func feedsFileView(pkg *spec.Package, servicex string) (*feedsFile, error) {
	if len(pkg.Feeds) == 0 {
		return nil, nil
	}
	im := newImports()
	im.std("context")
	im.use(servicex, "servicex")
	im.use("github.com/the-protobuf-project/runtime-go/events", "events")
	im.use("google.golang.org/protobuf/encoding/protojson", "protojson")
	f := &feedsFile{Package: pkg.GoPackage}
	for _, r := range pkg.Feeds {
		e := r.Events
		fv := feedView{
			GoName:    r.Message.GoIdent.GoName,
			PB:        im.qualify(r.Message),
			Repo:      im.use(r.Repository.Path, r.Repository.Name) + "." + r.RepoInterface,
			ID:        e.ID,
			Mask:      goList(e.Feed),
			Lifecycle: e.Lifecycle,
			Scope:     e.ScopeCollection,
		}
		if r.Parented {
			var parts []string
			for _, c := range r.ParentCollections() {
				parts = append(parts, c, "-")
			}
			fv.Wildcard = strings.Join(parts, "/")
		}
		for _, fd := range r.Message.Fields {
			switch fd.Desc.Name() {
			case "create_time":
				fv.CreateGet = getter(fd)
			case "delete_time":
				fv.DeleteGet = getter(fd)
			}
		}
		if e.State != nil {
			q := im.use(string(e.State.Enum.GoIdent.GoImportPath), pkgNames[e.State.Enum.GoIdent.GoImportPath])
			fv.StateEnum = q + "." + e.State.Enum.GoIdent.GoName
			fv.StateGet = getter(e.State)
			for _, st := range e.States {
				fv.States = append(fv.States, typesState{Const: fv.GoName + st.GoName + "Event", Value: q + "." + st.Value.GoIdent.GoName, Type: st.Type})
			}
		}
		f.Feeds = append(f.Feeds, fv)
	}
	lines, err := im.lines(pkg.Dir + "/feeds.svc.go")
	if err != nil {
		return nil, err
	}
	f.Imports = lines
	return f, nil
}

type errorsFile struct {
	Banner  string
	Package string
	Imports []string
	Domain  string
	Reasons []*spec.ErrorReason
	Enums   []string // full names, for the doc comment
	Proto   string
}

func errorsFileView(pkg *spec.Package, servicex string) (*errorsFile, error) {
	if len(pkg.Errors) == 0 {
		return nil, nil
	}
	im := newImports()
	im.use("github.com/the-protobuf-project/runtime-go/grpc/problem", "problem")
	im.use("google.golang.org/grpc/codes", "codes")
	im.use(servicex, "servicex")
	f := &errorsFile{Package: pkg.GoPackage, Domain: pkg.Errors[0].Domain, Proto: pkg.ProtoPackage}
	for _, e := range pkg.Errors {
		f.Enums = append(f.Enums, string(e.Enum.Desc.FullName()))
		f.Reasons = append(f.Reasons, e.Reasons...)
	}
	for _, r := range f.Reasons {
		if r.Sentinel {
			im.std("errors")
		}
	}
	lines, err := im.lines(pkg.Dir + "/errors.svc.go")
	if err != nil {
		return nil, err
	}
	f.Imports = lines
	return f, nil
}

type adaptersFile struct {
	Banner  string
	Package string
	Imports []string
	Events  *eventStoreView
	Idem    *idemView
}

// fieldView is one mapped field of an adapter's record.
type fieldView struct {
	GoName     string // struct field, "PositionTime"
	Get        string // getter, "GetPositionTime()"
	Proto      string // proto name, "position_time"
	OutputOnly bool
}

type eventStoreView struct {
	WebhookPB, DeliveryPB, CursorPB       string
	WebhookRepo, DeliveryRepo, CursorRepo string
	Webhook, Delivery, Cursor             map[string]*fieldView
	PausedValue                           string // qualified enum value, "" when paused is a bool or absent
	Pending, Succeeded, Failed            string
	StateEnumName                         string // proto enum name, for filter literals
	PendingLit, SucceededLit, FailedLit   string
	CursorCollection                      string
	WebhookWildcard                       string // "webhooks/-"
	WebhookSoftDelete                     bool
	DeliveryRemove                        string
}

type idemView struct {
	PB, Repo       string
	Remove         string
	F              map[string]*fieldView
	InFlight, Done string
}

func fieldsOf(a *spec.Adapter) map[string]*fieldView {
	out := map[string]*fieldView{}
	for k, fd := range a.Fields {
		out[k] = &fieldView{GoName: fd.GoName, Get: getter(fd), Proto: string(fd.Desc.Name()), OutputOnly: outputOnly(fd)}
	}
	return out
}

func outputOnly(fd *protogen.Field) bool {
	bs, _ := proto.GetExtension(fd.Desc.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, b := range bs {
		if b == annotations.FieldBehavior_OUTPUT_ONLY {
			return true
		}
	}
	return false
}

func adaptersFileView(pkg *spec.Package, servicex string) (*adaptersFile, []string, error) {
	if len(pkg.Adapters) == 0 {
		return nil, nil, nil
	}
	byRole := map[string]*spec.Adapter{}
	for _, a := range pkg.Adapters {
		byRole[a.Role] = a
	}
	im := newImports()
	im.std("context")
	im.use(servicex, "servicex")
	f := &adaptersFile{Package: pkg.GoPackage}
	var warnings []string
	enumValue := func(v *protogen.EnumValue) string {
		if v == nil {
			return ""
		}
		return im.use(string(v.GoIdent.GoImportPath), pkgNames[v.GoIdent.GoImportPath]) + "." + v.GoIdent.GoName
	}
	w, d, c := byRole["Webhook"], byRole["Delivery"], byRole["FeedCursor"]
	switch {
	case w != nil && d != nil && c != nil:
		im.use("github.com/the-protobuf-project/runtime-go/events", "events")
		im.std("time", "fmt")
		repox := repoxOf(w.Resource)
		im.use(repox, "repox")
		ev := &eventStoreView{
			WebhookPB: im.qualify(w.Resource.Message), DeliveryPB: im.qualify(d.Resource.Message), CursorPB: im.qualify(c.Resource.Message),
			WebhookRepo:       im.use(w.Resource.Repository.Path, w.Resource.Repository.Name) + "." + w.Resource.RepoInterface,
			DeliveryRepo:      im.use(d.Resource.Repository.Path, d.Resource.Repository.Name) + "." + d.Resource.RepoInterface,
			CursorRepo:        im.use(c.Resource.Repository.Path, c.Resource.Repository.Name) + "." + c.Resource.RepoInterface,
			Webhook:           fieldsOf(w),
			Delivery:          fieldsOf(d),
			Cursor:            fieldsOf(c),
			PausedValue:       enumValue(w.States["PAUSED"]),
			Pending:           enumValue(d.States["PENDING"]),
			Succeeded:         enumValue(d.States["SUCCEEDED"]),
			Failed:            enumValue(d.States["FAILED"]),
			PendingLit:        string(d.States["PENDING"].Desc.Name()),
			SucceededLit:      string(d.States["SUCCEEDED"].Desc.Name()),
			FailedLit:         string(d.States["FAILED"].Desc.Name()),
			CursorCollection:  c.Resource.Collection(),
			WebhookWildcard:   d.Resource.Segments[0].Collection + "/-",
			WebhookSoftDelete: w.Resource.SoftDelete,
			DeliveryRemove:    removeOf(d.Resource),
		}
		if ev.Webhook["paused"] != nil {
			ev.PausedValue = ""
		}
		f.Events = ev
	case w != nil || d != nil || c != nil:
		warnings = append(warnings, "package "+pkg.ProtoPackage+" marks some but not all of the webhook, delivery and feed cursor roles; the events.Store adapter needs all three in one package, so none is generated")
	}
	if k := byRole["IdempotencyKey"]; k != nil {
		f.Idem = &idemView{
			PB:       im.qualify(k.Resource.Message),
			Repo:     im.use(k.Resource.Repository.Path, k.Resource.Repository.Name) + "." + k.Resource.RepoInterface,
			Remove:   removeOf(k.Resource),
			F:        fieldsOf(k),
			InFlight: enumValue(k.States["IN_FLIGHT"]),
			Done:     enumValue(k.States["DONE"]),
		}
		im.use("github.com/the-protobuf-project/runtime-go/grpc/idempotency", "idempotency")
	}
	if f.Events == nil && f.Idem == nil {
		return nil, warnings, nil
	}
	lines, err := im.lines(pkg.Dir + "/adapters.svc.go")
	if err != nil {
		return nil, nil, err
	}
	f.Imports = lines
	return f, warnings, nil
}

// repoxOf is the repox package beside r's repository.
func repoxOf(r *spec.Resource) string {
	p := r.Repository.Path
	p = p[:strings.LastIndex(p, "/")]
	p = p[:strings.LastIndex(p, "/")]
	return p + "/repox"
}

// removeOf is the repository call that removes one of r's records for good.
func removeOf(r *spec.Resource) string {
	if r.SoftDelete {
		return "Purge"
	}
	return "Delete"
}

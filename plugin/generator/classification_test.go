// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0
package generator_test

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/service/plugin/generator"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomListNameStaysCustom(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "directory"))
	for _, f := range req.ProtoFile {
		for _, s := range f.Service {
			for _, m := range s.Method {
				if m.GetName() == "ListOrganizations" {
					m.Name = proto.String("ListRecommendedOrganizations")
				}
			}
		}
	}
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, casePlugin("")); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Response().File {
		if strings.Contains(f.GetContent(), "func (s *OrganizationsServer) ListRecommendedOrganizations(") {
			t.Fatal("custom list generated as an unfiltered standard list")
		}
	}
}
func TestAdapterStateTypeRejected(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "webhook"))
	for _, f := range req.ProtoFile {
		for _, m := range f.MessageType {
			if m.GetName() == "Delivery" {
				for _, fd := range m.Field {
					if fd.GetName() == "state" {
						fd.Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
						fd.TypeName = nil
					}
				}
			}
		}
	}
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	err = protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, casePlugin(""))
	if err == nil || !strings.Contains(err.Error(), "field state has an incompatible type") {
		t.Fatalf("got %v", err)
	}
}

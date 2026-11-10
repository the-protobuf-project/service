// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/service/plugin/spec"
)

func (t *Target) service(b *strings.Builder, s *spec.Service) error {
	service := s.GoName
	server := service + "Server"
	pb := t.Settings.ProtoModule
	for _, v := range strings.Split(string(s.Proto.Desc.ParentFile().Package()), ".") {
		pb += "::" + ident(v)
	}
	trait := pb + "::" + ident(service) + "_server::" + service

	custom := false
	for _, m := range s.Methods {
		if m.Kind == spec.Custom {
			custom = true
		}
	}
	if custom {
		fmt.Fprintf(b, "/// Custom RPC handlers for %s. Override the methods your application serves.\n#[tonic::async_trait]\npub trait %sCustom: Send + Sync {\n", service, service)
		for _, m := range s.Methods {
			if m.Kind != spec.Custom {
				continue
			}
			in, out := t.typ(m.Proto.Input), t.typ(m.Proto.Output)
			if m.Proto.Desc.IsStreamingClient() {
				in = "tonic::Streaming<" + in + ">"
			}
			if m.Proto.Desc.IsStreamingServer() {
				out = "servicex::ResponseStream<" + out + ">"
			}
			fmt.Fprintf(b, "async fn %s(&self, _request:tonic::Request<%s>)->Result<tonic::Response<%s>,tonic::Status>{Err(tonic::Status::unimplemented(%s))}\n", ident(m.Proto.GoName), in, out, q(m.Proto.GoName))
		}
		fmt.Fprintf(b, "}\n\n/// Default custom handlers returning `Unimplemented`.\npub struct Nop%sCustom;\n\nimpl %sCustom for Nop%sCustom {}\n\n", service, service, service)
	}
	fmt.Fprintf(b, "/// Generated server backed by typed repositories and application hooks.\npub struct %s {\npub options: servicex::Options,\n", server)
	if custom {
		fmt.Fprintf(b, "pub custom: std::sync::Arc<dyn %sCustom>,\n", service)
	}
	for _, r := range s.Resources {
		fmt.Fprintf(b, "pub %s: servicex::Resource<%s>,\n", ident(r.Message.GoIdent.GoName), t.typ(r.Message))
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(b, "impl %s {\n/// Creates a server over the supplied resources.\npub fn new(options:servicex::Options", server)
	for _, r := range s.Resources {
		fmt.Fprintf(b, ", %s:servicex::Resource<%s>", ident(r.Message.GoIdent.GoName), t.typ(r.Message))
	}
	b.WriteString(")->Self {Self {options,")
	for _, r := range s.Resources {
		fmt.Fprintf(b, "%s,", ident(r.Message.GoIdent.GoName))
	}
	if custom {
		fmt.Fprintf(b, "custom:std::sync::Arc::new(Nop%sCustom),", service)
	}
	b.WriteString("}}\n\n")
	if custom {
		fmt.Fprintf(b, "/// Installs the application's custom RPC handlers.\npub fn with_custom(mut self,custom:impl %sCustom+'static)->Self{self.custom=std::sync::Arc::new(custom);self}\n", service)
	}
	fmt.Fprintf(b, "/// Wraps this server for registration with tonic.\npub fn into_service(self) -> %s_server::%sServer<Self> { %s_server::%sServer::new(self) }\n}\n\n", pb+"::"+ident(service), service, pb+"::"+ident(service), service)
	fmt.Fprintf(b, "#[tonic::async_trait]\nimpl %s for %s {\n", trait, server)
	for _, m := range s.Methods {
		mn := ident(m.Proto.GoName)
		in, out := t.typ(m.Proto.Input), t.typ(m.Proto.Output)
		if m.Proto.Desc.IsStreamingClient() {
			in = "tonic::Streaming<" + in + ">"
		}
		if m.Proto.Desc.IsStreamingServer() {
			assoc := m.Proto.GoName + "Stream"
			fmt.Fprintf(b, "type %s = servicex::ResponseStream<%s>;\n", assoc, out)
			out = "Self::" + assoc
		}
		fmt.Fprintf(b, "async fn %s(&self, request: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status> {\n", mn, in, out)
		if m.Kind == spec.Custom {
			fmt.Fprintf(b, "self.custom.%s(request).await\n}\n\n", mn)
			continue
		}
		r := m.Resource
		fmt.Fprintf(b, "let ctx = servicex::Context::from_request(&request);\nlet req = request.into_inner();\nlet resource = &self.%s;\nlet spec = servicex::ResourceSpec { message: %s, resource_type: %s, collections: %s, required: %s, output_only: %s, immutable: %s, soft_delete: %t };\n", ident(r.Message.GoIdent.GoName), q(string(r.Message.Desc.FullName())), q(r.Type), arr(r.Collections()), arr(r.Required), arr(r.OutputOnly), arr(r.Immutable), r.SoftDelete)
		fmt.Fprintf(b, "let options = self.options.with_domain(%s);\nlet call = servicex::Call::new(resource, &options, &spec, &ctx);\n", q(s.Domain))
		switch m.Kind {
		case spec.Get:
			fmt.Fprintf(b, "let out = call.get(%s).await?;\n", borrowed(m.Name))
		case spec.List:
			defaults := ""
			if len(m.Proto.Output.Fields) > 2 {
				defaults = ", ..Default::default()"
			}
			mask := "Vec::new()"
			if m.ReadMask != nil {
				mask = "req." + field(m.ReadMask) + ".map(|m| m.paths).unwrap_or_default()"
			}
			fmt.Fprintf(b, "let (items,next) = call.list(%s,servicex::ListInput { page_size:%s,page_token:%s,filter:%s,order_by:%s,show_deleted:%s,read_mask:%s }).await?;\nlet out = %s { %s:items, %s:next%s };\n", borrowed(m.Parent), scalar(m.PageSize, "0"), scalar(m.PageToken, "String::new()"), scalar(m.Filter, "String::new()"), scalar(m.OrderBy, "String::new()"), scalar(m.ShowDeleted, "false"), mask, t.typ(m.Proto.Output), field(m.Items), field(m.NextPageToken), defaults)
		case spec.Create:
			fmt.Fprintf(b, "let body = req.%s.ok_or_else(||options.bad_field(\"REQUIRED_FIELD\",%s,\"resource is required\"))?;\nlet out = call.create(%s,%s,body,%s).await?;\n", field(m.Body), q(string(m.Body.Desc.Name())), borrowed(m.Parent), borrowed(m.ID), scalar(m.ValidateOnly, "false"))
		case spec.Update:
			mask := "None"
			if m.UpdateMask != nil {
				mask = "req." + field(m.UpdateMask) + ".map(|m| m.paths)"
			}
			fmt.Fprintf(b, "let body = req.%s.ok_or_else(||options.bad_field(\"REQUIRED_FIELD\",%s,\"resource is required\"))?;\nlet out = call.update(body,%s,%s,%s).await?;\n", field(m.Body), q(string(m.Body.Desc.Name())), mask, scalar(m.AllowMissing, "false"), scalar(m.ValidateOnly, "false"))
		case spec.Delete:
			fmt.Fprintf(b, "let deleted = call.delete(%s,%s,%s,%s,%s).await?;\n", borrowed(m.Name), borrowed(m.Etag), scalar(m.AllowMissing, "false"), scalar(m.ValidateOnly, "false"), scalar(m.Force, "false"))
			if m.ReturnsEmpty {
				b.WriteString("let _ = deleted; let out = ();\n")
			} else {
				b.WriteString("let out = deleted.unwrap_or_default();\n")
			}
		case spec.Undelete:
			fmt.Fprintf(b, "let out = call.undelete(%s,%s,%s).await?;\n", borrowed(m.Name), borrowed(m.Etag), scalar(m.ValidateOnly, "false"))
		case spec.Purge:
			fmt.Fprintf(b, "call.purge(%s).await?; let out = ();\n", borrowed(m.Name))
		}
		b.WriteString("Ok(tonic::Response::new(out))\n}\n\n")
	}
	b.WriteString("}\n")
	return nil
}

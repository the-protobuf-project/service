// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/spec"
)

// service renders one service: its handlers -- one per RPC, the request in
// and the response out, registered by the application -- and the server that
// runs each standard method's generated checks and hooks around its handler.
func (t *Target) service(b *strings.Builder, s *spec.Service) error {
	service := s.GoName
	server := service + "Server"
	handlers := service + "Handlers"
	pb := t.Settings.ProtoModule
	for _, v := range strings.Split(string(s.Proto.Desc.ParentFile().Package()), ".") {
		pb += "::" + ident(v)
	}
	trait := pb + "::" + ident(service) + "_server::" + service

	// The handlers.
	fmt.Fprintf(b, "/// The handlers of %s: one per RPC, the request message in and the response\n/// message out -- the database layer, a call to another service, any business logic.\n/// The server runs the generated checks and hooks of a standard method around its handler.\n#[derive(Default, Clone)]\npub struct %s {\n", s.FullName, handlers)
	for _, m := range s.Methods {
		in, out := t.signature(m)
		fmt.Fprintf(b, "/// %s.\npub %s: Option<servicex::Handler<%s, %s>>,\n", m.Proto.GoName, ident(m.Proto.GoName), in, out)
	}
	fmt.Fprintf(b, "}\n\nimpl %s {\n", handlers)
	for _, m := range s.Methods {
		in, out := t.signature(m)
		fmt.Fprintf(b, "/// Registers the handler of %s.\npub fn %s<F, Fut>(mut self, f: F) -> Self\nwhere\nF: Fn(servicex::Context, %s) -> Fut + Send + Sync + 'static,\nFut: std::future::Future<Output = Result<%s, tonic::Status>> + Send + 'static,\n{\nself.%s = Some(servicex::Handler::new(f));\nself\n}\n", m.Proto.GoName, ident(m.Proto.GoName), in, out, ident(m.Proto.GoName))
	}
	b.WriteString("/// The RPCs no handler is registered for.\npub fn missing(&self) -> Vec<&'static str> {\nlet mut out = Vec::new();\n")
	for _, m := range s.Methods {
		fmt.Fprintf(b, "if self.%s.is_none() { out.push(%s); }\n", ident(m.Proto.GoName), q(m.Proto.GoName))
	}
	b.WriteString("out\n}\n}\n\n")

	// The server.
	fmt.Fprintf(b, "/// %s's server: each standard method's generated checks and hooks around its\n/// registered handler, and every custom method straight to its own.\npub struct %s {\npub options: servicex::Options,\nhandlers: %s,\n", s.FullName, server, handlers)
	for _, r := range s.Resources {
		fmt.Fprintf(b, "%s_hooks: std::sync::Arc<dyn servicex::Hooks<%s>>,\n", ident(r.Message.GoIdent.GoName), t.typ(r.Message))
	}
	fmt.Fprintf(b, "}\n\nimpl %s {\n/// Builds the server over handlers, refusing one that leaves an RPC without a handler.\npub fn new(options: servicex::Options, handlers: %s) -> Result<Self, servicex::MissingHandlers> {\nlet methods = handlers.missing();\nif !methods.is_empty() {\nreturn Err(servicex::MissingHandlers { service: %s, methods });\n}\nOk(Self { options, handlers,", server, handlers, q(s.FullName))
	for _, r := range s.Resources {
		fmt.Fprintf(b, "%s_hooks: std::sync::Arc::new(servicex::NopHooks),", ident(r.Message.GoIdent.GoName))
	}
	b.WriteString("})\n}\n")
	for _, r := range s.Resources {
		res := ident(r.Message.GoIdent.GoName)
		fmt.Fprintf(b, "/// Sets the hooks around %s's standard methods.\npub fn with_%s_hooks(mut self, hooks: impl servicex::Hooks<%s> + 'static) -> Self {\nself.%s_hooks = std::sync::Arc::new(hooks);\nself\n}\n", r.Message.GoIdent.GoName, res, t.typ(r.Message), res)
		fmt.Fprintf(b, "/// Sets hooks shared with other owners around %s's standard methods.\npub fn with_%s_hooks_shared(mut self, hooks: std::sync::Arc<dyn servicex::Hooks<%s>>) -> Self {\nself.%s_hooks = hooks;\nself\n}\n", r.Message.GoIdent.GoName, res, t.typ(r.Message), res)
	}
	fmt.Fprintf(b, "/// Wraps this server for registration with tonic.\npub fn into_service(self) -> %s_server::%sServer<Self> { %s_server::%sServer::new(self) }\n}\n\n", pb+"::"+ident(service), service, pb+"::"+ident(service), service)

	fmt.Fprintf(b, "#[tonic::async_trait]\nimpl %s for %s {\n", trait, server)
	for _, m := range s.Methods {
		mn := ident(m.Proto.GoName)
		in, out := t.signature(m)
		if m.Proto.Desc.IsStreamingServer() {
			assoc := m.Proto.GoName + "Stream"
			fmt.Fprintf(b, "type %s = %s;\n", assoc, out)
			out = "Self::" + assoc
		}
		fmt.Fprintf(b, "async fn %s(&self, request: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status> {\nlet ctx = servicex::Context::from_request(&request);\n", mn, in, out)
		if m.Kind == spec.Custom {
			fmt.Fprintf(b, "let out = servicex::handler(&self.handlers.%s, %s)?.call(ctx, request.into_inner()).await?;\nOk(tonic::Response::new(out))\n}\n\n", mn, q(m.Proto.GoName))
			continue
		}
		t.standard(b, s, m)
		b.WriteString("Ok(tonic::Response::new(out))\n}\n\n")
	}
	b.WriteString("}\n")
	if t.Settings.Store && s.Standard() {
		t.repositories(b, s)
	}
	return nil
}

// signature is a method's handler input and output: the messages, a client
// stream for a client-streaming request, a response stream for a
// server-streaming response.
func (t *Target) signature(m *spec.Method) (in, out string) {
	in, out = t.typ(m.Proto.Input), t.typ(m.Proto.Output)
	if m.Proto.Desc.IsStreamingClient() {
		in = "tonic::Streaming<" + in + ">"
	}
	if m.Proto.Desc.IsStreamingServer() {
		out = "servicex::ResponseStream<" + out + ">"
	}
	return in, out
}

// standard renders a standard method's body up to the response: its checks
// and hooks (servicex::Call) around its handler, binding out.
func (t *Target) standard(b *strings.Builder, s *spec.Service, m *spec.Method) {
	r := m.Resource
	res := ident(r.Message.GoIdent.GoName)
	mutable := m.Kind == spec.Create || m.Kind == spec.Update
	if mutable {
		b.WriteString("let mut req = request.into_inner();\n")
	} else {
		b.WriteString("let req = request.into_inner();\n")
	}
	fmt.Fprintf(b, "let spec = servicex::ResourceSpec { message: %s, resource_type: %s, collections: %s, required: %s, output_only: %s, immutable: %s, soft_delete: %t };\n", q(string(r.Message.Desc.FullName())), q(r.Type), arr(r.Collections()), arr(r.Required), arr(r.OutputOnly), arr(r.Immutable), r.SoftDelete)
	fmt.Fprintf(b, "let options = self.options.with_domain(%s);\nlet call = servicex::Call::new(self.%s_hooks.as_ref(), &options, &spec, &ctx);\nlet handler = servicex::handler(&self.handlers.%s, %s)?;\n", q(s.Domain), res, ident(m.Proto.GoName), q(m.Proto.GoName))
	run := "handler.call(ctx.clone(), req).await.map_err(|e| call.mapped(e))?"
	dry := scalar(m.ValidateOnly, "false")

	// existing reads the stored record with the service's Get, when it has one.
	existing := func(name string) string {
		for _, g := range s.Methods {
			if g.Kind == spec.Get && g.Resource == r {
				rest := ", ..Default::default()"
				if len(g.Proto.Input.Fields) == 1 {
					rest = "" // the name is the whole request
				}
				return fmt.Sprintf("match &self.handlers.%s {\nSome(get) => Some(get.call(ctx.clone(), %s { %s: %s%s }).await),\nNone => None,\n}", ident(g.Proto.GoName), t.typ(g.Proto.Input), field(g.Name), name, rest)
			}
		}
		return "None"
	}

	switch m.Kind {
	case spec.Get:
		fmt.Fprintf(b, "call.name(%s)?;\nlet out = %s;\n", borrowed(m.Name), run)
	case spec.Purge:
		fmt.Fprintf(b, "call.name(%s)?;\nlet out = %s;\n", borrowed(m.Name), run)
	case spec.List:
		parent := `""`
		if m.Parent != nil {
			parent = borrowed(m.Parent)
		}
		fmt.Fprintf(b, "call.list(%s, %s).await?;\nlet out = %s;\n", parent, scalar(m.PageSize, "0"), run)
	case spec.Create:
		fmt.Fprintf(b, "let body = req.%s.take().ok_or_else(|| options.bad_field(\"REQUIRED_FIELD\", %s, \"resource is required\"))?;\n", field(m.Body), q(string(m.Body.Desc.Name())))
		parent := `""`
		if m.Parent != nil {
			parent = borrowed(m.Parent)
		}
		fmt.Fprintf(b, "let body = call.create(%s, %s, body).await?;\nif %s {\nreturn Ok(tonic::Response::new(body));\n}\nreq.%s = Some(body);\nlet out = %s;\ncall.after_create(&out).await?;\n", parent, borrowed(m.ID), dry, field(m.Body), run)
	case spec.Update:
		mask := "None"
		if m.UpdateMask != nil {
			mask = "req." + field(m.UpdateMask) + ".clone().map(|m| m.paths)"
		}
		fmt.Fprintf(b, "let body = req.%s.clone().ok_or_else(|| options.bad_field(\"REQUIRED_FIELD\", %s, \"resource is required\"))?;\nlet existing = %s;\nlet dry = %s;\n", field(m.Body), q(string(m.Body.Desc.Name())), existing("body.name.clone()"), dry)
		writeMask := "let _ = paths;\n"
		if m.UpdateMask != nil {
			writeMask = "req." + field(m.UpdateMask) + " = Some(prost_types::FieldMask { paths });\n"
		}
		fmt.Fprintf(b, "let out = match call.update(body, %s, %s, existing).await? {\nservicex::Update::Create(value) => {\nif dry {\nreturn Ok(tonic::Response::new(value));\n}\nreq.%s = Some(value);\nlet out = %s;\ncall.after_create(&out).await?;\nout\n}\nservicex::Update::Write { value, paths, preview } => {\nif dry {\nreturn Ok(tonic::Response::new(preview));\n}\nreq.%s = Some(value);\n%slet out = %s;\ncall.after_update(&out).await?;\nout\n}\n};\n", mask, scalar(m.AllowMissing, "false"), field(m.Body), run, field(m.Body), writeMask, run)
	case spec.Delete:
		skipped := "()"
		preview := "()"
		bind := "_"
		if !m.ReturnsEmpty {
			bind = "existing"
			skipped = t.typ(r.Message) + " { name: name.clone(), ..Default::default() }"
			preview = "existing.unwrap_or_default()"
		}
		allow := scalar(m.AllowMissing, "false")
		fmt.Fprintf(b, "let name = req.%s.clone();\nlet existing = %s;\nlet allow_missing = %s;\nmatch call.delete(&name, %s, allow_missing, %s, existing).await? {\nservicex::Delete::Skip => return Ok(tonic::Response::new(%s)),\nservicex::Delete::Proceed(%s) => {\nif %s {\nreturn Ok(tonic::Response::new(%s));\n}\n}\n}\nlet out = match handler.call(ctx.clone(), req).await {\nErr(e) if allow_missing && e.code() == tonic::Code::NotFound => return Ok(tonic::Response::new(%s)),\nresult => result.map_err(|e| call.mapped(e))?,\n};\ncall.after_delete(&name).await?;\n", field(m.Name), existing("name.clone()"), allow, borrowed(m.Etag), scalar(m.Force, "false"), skipped, bind, dry, preview, skipped)
	case spec.Undelete:
		fmt.Fprintf(b, "let name = req.%s.clone();\nlet existing = %s;\nlet preview = call.undelete(&name, %s, existing).await?;\nif %s {\nreturn Ok(tonic::Response::new(preview.unwrap_or_default()));\n}\nlet out = %s;\n", field(m.Name), existing("name.clone()"), borrowed(m.Etag), dry, run)
	}
}

// repoField is r's field in <S>Repositories, and in protoc-gen-store's
// per-package Repositories: its plural, snake-cased ("organizational_units").
func repoField(r *spec.Resource) string {
	return ident(naming.Pluralize(naming.SnakeCase(r.Message.GoIdent.GoName)))
}

// repositories renders store=true's <S>Repositories and the handlers over
// them: every standard method answered by a runtime-rs repository, with the
// semantics of the Go repository-backed servers.
func (t *Target) repositories(b *strings.Builder, s *spec.Service) {
	repos := s.GoName + "Repositories"
	fmt.Fprintf(b, "\n/// The repositories %sServer persists through with store=true, one per\n/// resource: protoc-gen-store's (`database::repository::Repository`), or a cache\n/// decorator in front of one. The twin of the Go server's %s.\n#[derive(Clone)]\npub struct %s {\n", s.GoName, repos, repos)
	for _, r := range s.Resources {
		fmt.Fprintf(b, "/// %s.\npub %s: servicex::Repo<%s>,\n", r.Message.GoIdent.GoName, repoField(r), t.typ(r.Message))
	}
	fmt.Fprintf(b, "}\n\nimpl %sHandlers {\n/// Answers every standard method from repos; the custom methods stay yours to\n/// register. The server's checks and hooks run around these as around any handler:\n/// the repository mints ids and etags, guards a write with the etag it carries,\n/// soft-deletes, and pages, filters and orders a List.\npub fn with_repositories(mut self, repos: &%s) -> Self {\n", s.GoName, repos)
	for _, m := range s.Methods {
		if m.Kind == spec.Custom {
			continue
		}
		r := m.Resource
		in, _ := t.signature(m)
		typ := q(r.Type)
		mn := ident(m.Proto.GoName)
		var body string
		switch m.Kind {
		case spec.Get:
			body = fmt.Sprintf("let name = req.%s;\nrepo.get(&name).await.map_err(|e| servicex::store_status(e, %s, &name))", field(m.Name), typ)
		case spec.List:
			parent := `""`
			if m.Parent != nil {
				parent = borrowed(m.Parent)
			}
			filter := `""`
			if m.Filter != nil {
				filter = borrowed(m.Filter)
			}
			orderBy := "String::new()"
			if m.OrderBy != nil {
				orderBy = "String::from(" + borrowed(m.OrderBy) + ")"
			}
			token := "String::new()"
			if m.PageToken != nil {
				token = "String::from(" + borrowed(m.PageToken) + ")"
			}
			rest := ""
			if len(m.Proto.Output.Fields) > 2 {
				rest = "\n..Default::default()"
			}
			body = fmt.Sprintf("let input = ::database::repository::ListInput {\npage_size: %s,\npage_token: %s,\norder_by: %s,\nfilter: String::from(%s),\nshow_deleted: %s,\nparent: String::from(%s),\n};\nlet page = repo.list(&input).await.map_err(|e| servicex::store_status(e, %s, \"\"))?;\nOk(%s {\n%s: page.items,\n%s: page.next_page_token,%s\n})",
				scalar(m.PageSize, "0"), token, orderBy, filter, scalar(m.ShowDeleted, "false"), parent, typ,
				t.typ(m.Proto.Output), field(m.Items), field(m.NextPageToken), rest)
		case spec.Create:
			parent := `""`
			if m.Parent != nil {
				parent = borrowed(m.Parent)
			}
			// No id chosen: the repository mints one (a ULID, as Go's
			// repox does), under the parent when the name ends in
			// "<collection>/".
			mint := ""
			if m.Parent != nil {
				mint = fmt.Sprintf("if value.name.is_empty() {\nvalue.name = format!(\"{}/%s/\", %s);\n}\n", r.Collection(), strings.TrimPrefix(parent, "&"))
			}
			binding := "let value"
			if mint != "" {
				binding = "let mut value"
			}
			body = fmt.Sprintf("%s = req.%s.clone().unwrap_or_default();\n%srepo.create(&value).await.map_err(|e| servicex::store_status(e, %s, &value.name))", binding, field(m.Body), mint, typ)
		case spec.Update:
			mask := "Vec::new()"
			if m.UpdateMask != nil {
				mask = "servicex::store_paths(req." + field(m.UpdateMask) + ")"
			}
			write := "repo.update(&value, &paths).await"
			if m.AllowMissing != nil {
				// The server decided allow_missing's create; the update
				// finding nothing is that case.
				write = "match repo.update(&value, &paths).await {\nErr(::database::Error::NotFound) if allow_missing => repo.create(&value).await,\nresult => result,\n}"
				body = "let allow_missing = " + scalar(m.AllowMissing, "false") + ";\n"
			}
			body += fmt.Sprintf("let value = req.%s.unwrap_or_default();\nlet paths = %s;\n%s.map_err(|e| servicex::store_status(e, %s, &value.name))", field(m.Body), mask, write, typ)
		case spec.Delete:
			etag := `""`
			if m.Etag != nil {
				etag = borrowed(m.Etag)
			}
			body = fmt.Sprintf("let name = req.%s.clone();\nrepo.delete(&name, servicex::if_match(%s)).await.map_err(|e| servicex::store_status(e, %s, &name))?;\n", field(m.Name), etag, typ)
			if m.ReturnsEmpty {
				body += "Ok(())"
			} else {
				// The deleted record, as a soft delete keeps it; a hard
				// delete leaves only its name.
				body += fmt.Sprintf("match repo.get(&name).await {\nOk(value) => Ok(value),\nErr(::database::Error::NotFound) => Ok(%s { name, ..Default::default() }),\nErr(e) => Err(servicex::store_status(e, %s, &name)),\n}", t.typ(r.Message), typ)
			}
		case spec.Undelete:
			etag := `""`
			if m.Etag != nil {
				etag = borrowed(m.Etag)
			}
			body = fmt.Sprintf("let name = req.%s.clone();\nrepo.undelete(&name, servicex::if_match(%s)).await.map_err(|e| servicex::store_status(e, %s, &name))", field(m.Name), etag, typ)
		case spec.Purge:
			body = fmt.Sprintf("let name = req.%s;\nrepo.purge(&name).await.map_err(|e| servicex::store_status(e, %s, &name))", field(m.Name), typ)
		}
		fmt.Fprintf(b, "{\nlet repo = repos.%s.clone();\nself.%s = Some(servicex::Handler::new(move |_ctx, req: %s| {\nlet repo = repo.clone();\nasync move {\n%s\n}\n}));\n}\n", repoField(r), mn, in, body)
	}
	b.WriteString("self\n}\n}\n")
}

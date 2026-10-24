// protoc-gen-service: the service.v1 vocabulary and the generator that emits
// the AIP standard-method service layer over protoc-gen-store's generated
// repositories.
//
// The dependency set is a statement of what this plugin is allowed to know:
//
//	protokit   the neutral IR engine, pinned, so the database and schema names
//	           this plugin derives for a resource are the ones protoc-gen-store
//	           derived when it wrote that resource's repository.
//	store      plugin/entity, the shared layout reader, imported rather than
//	           reimplemented for the same reason; and, in tests only, the store
//	           generator itself, to prove agreement and compile the two outputs
//	           together.
//
// Notably absent: runtime-go. This plugin emits code that imports
// runtime-go/grpc (the registration helpers); it does not link against it.
module github.com/the-protobuf-project/service

go 1.26.5

require (
	github.com/the-protobuf-project/protokit v1.3.4
	github.com/the-protobuf-project/store v1.6.3
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/agnivade/levenshtein v1.2.2 // indirect
	github.com/bufbuild/protocompile v0.14.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/the-protobuf-project/telemetry/telemetry-go v0.0.0-20261021141513-5a35ba0e3bb7 // indirect
	github.com/vektah/gqlparser/v2 v2.5.63 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/grpc v1.84.0 // indirect
)

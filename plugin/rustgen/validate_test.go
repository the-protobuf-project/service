// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen_test

// The example protos import buf/validate/validate.proto; the golden compiler
// resolves imports from the linked registry, so link it.
import _ "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"

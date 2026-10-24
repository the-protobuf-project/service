#!/usr/bin/env bash
# Run the Google API linter over every proto this repository owns.
#
# There is no configuration file and no disabled rule. protoc-gen-cache reads
# google.api.resource to derive a namespace, and refuses outright when a resource
# pattern nests under a parent nothing binds — so it holds its users to AIP, and
# a vocabulary that did not itself pass the linter would be ducking the bar it
# sets. The examples are held to the same standard because they are what a reader
# copies.
#
# api-linter reads a FileDescriptorSet rather than resolving imports itself, so
# buf builds one per module first.
#
# plugin/generator/testdata is deliberately not linted here, and it is worth
# saying why rather than leaving it to look like an oversight. Those fixtures are
# organised by *test case* — errors/unbound_parent, cases/warnings — so their
# directories do not mirror their proto packages, and two cases legitimately share
# the package tenancy.v1 because they are separate cases. That trips
# core::0191::proto-package and buf'"'"'s own PACKAGE_DIRECTORY_MATCH, neither of
# which is telling us anything true: the layout is right for what those files are.
#
# They are still kept AIP-clean in every other respect (java options, comments on
# fields), because an editor lints them on open and a fixture nobody can read
# without dismissing warnings is a fixture nobody reads.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

status=0

lint_module() {
  local module="$1"
  # Two statements, not one: bash expands the whole assignment list before
  # binding any of it, so a `local a=$1 b=$a` reads an unset $a under `set -u`.
  local set="$tmp/${module//\//_}.binpb"
  buf build "$root/$module" -o "$set" --as-file-descriptor-set

  # This module's own files, relative to its module root — not its dependencies',
  # which are someone else's to fix.
  local files=()
  while IFS= read -r f; do files+=("$f"); done < <(
    cd "$root/$module" && find . -name '*.proto' | sed 's|^\./||' | sort
  )

  echo "==> $module (${#files[@]} files)"
  if ! api-linter --descriptor-set-in="$set" --set-exit-status "${files[@]}"; then
    status=1
  fi
}

lint_module protobuf
lint_module examples/proto

if [ "$status" -eq 0 ]; then
  echo "==> clean: no AIP findings"
fi
exit "$status"

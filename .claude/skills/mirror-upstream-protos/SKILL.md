---
name: mirror-upstream-protos
description: Use when bringing new streamable fields, enum values or Value oneof members from an upstream teslamotors/fleet-telemetry proto change into this fork's protos/.
---

# Mirror upstream proto changes

The fork's generated `*.pb.go` differ in layout from upstream's, so upstream's generated hunks do not apply. Apply only the `.proto` hunks, then regenerate with the pinned toolchain.

1. **Fetch the upstream change.** The upstream PR may still be open; that is fine.
   ```bash
   git fetch upstream main pull/<n>/head:upstream-pr-<n>
   BASE=$(git merge-base upstream/main upstream-pr-<n>)
   git diff $BASE upstream-pr-<n> -- 'protos/*.proto' | git apply
   ```
   Check that the field numbers do not collide with anything already in `protos/vehicle_data.proto`.
2. **Use the pinned toolchain.** CI uses `protoc` 28.3 and `protoc-gen-go` v1.28.1:
   ```bash
   protoc --version   # libprotoc 28.3
   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.28.1
   ```
   `protoc-gen-go` must be first on `PATH` (`$(go env GOPATH)/bin`). A newer `protoc-gen-go` rewrites every `*.pb.go`, and CI's `make generate-protos && git diff --exit-code` gate fails.
3. **Regenerate.**
   ```bash
   make generate-protos
   git status --short protos/
   ```
   Only the `.proto` you edited and its `.pb.go`, `python/*_pb2.py` and `ruby/*_pb.rb` should change. If every `*.pb.go` changed, the `protoc-gen-go` version is wrong.
4. **Cross-check against upstream.** If you applied the whole upstream `.proto` hunk, the Python and Ruby output is byte-identical to upstream's:
   ```bash
   git diff $BASE upstream-pr-<n> -- protos/python protos/ruby | git apply -R --check
   ```
5. **New `Value` oneof members.** For each new `Value_<X>Value`, add a case to `transformValue` in `datastore/simple/transformers/payload.go`. Its `default` returns `ok=false`, so the logger dispatcher silently drops a field without a case. (`datastore/mqtt/mqtt_payload.go` falls back to `getProtoValue`, so it needs no case.)
6. **Validate:** `make format && make linters && make test`.
7. **Open the PR.** Title: `protos: add streamable fields <first>-<last> (mirrors upstream PR <n>)`. In the body, link the upstream PR, say whether it is still open upstream, and add an upstream-merge note for anything fork-only (such as the transformer cases) so a later `teslamotors/main` merge can resolve it.

An open upstream PR can be force-pushed after you mirror it. Before cutting a release with mirrored fields, and again when the upstream PR merges, diff it against the fork (`git diff upstream-pr-<n> HEAD -- protos/vehicle_data.proto`). A changed field number breaks the wire format. A renamed field changes the JSON key that `transmit_decoded_records` emits. Mirror either change in a follow-up PR.

Rolling out a server that emits the new fields is a separate task (see the `cut-release` skill).

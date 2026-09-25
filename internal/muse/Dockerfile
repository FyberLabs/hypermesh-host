# hypermesh-muse: one image, three placements.
# HYPERMESH_MUSE_PLACEMENT=outside | host | renter
# outside — off the network; holds credentials and restarts the local runtime.
# host — enrolls and heartbeats as a supervisor host; refuses lease_start.
# renter — POST /v1/chat/completions on the chat gateway, not the control-plane stub.
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/hypermesh-muse ./cmd/hypermesh-muse

FROM ubuntu:24.04
COPY --from=build /out/hypermesh-muse /usr/local/bin/hypermesh-muse
ENV HYPERMESH_MUSE_PLACEMENT=outside
ENTRYPOINT ["/usr/local/bin/hypermesh-muse"]

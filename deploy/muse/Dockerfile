# hypermesh-muse: one image, three placements.
# HYPERMESH_MUSE_PLACEMENT=outside | host | renter
# outside — off the network; restarts the local runtime.
# host — enrolls and heartbeats as a supervisor host; refuses lease_start.
# renter — POST /v1/chat/completions on the chat gateway, not the control-plane stub.
# The runtime image is Alpine. The binary is CGO_ENABLED=0.
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/hypermesh-muse ./cmd/hypermesh-muse

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/hypermesh-muse /usr/local/bin/hypermesh-muse
ENV HYPERMESH_MUSE_PLACEMENT=outside
ENTRYPOINT ["/usr/local/bin/hypermesh-muse"]

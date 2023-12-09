# Build the manager binary
FROM golang:alpine as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.sum ./
COPY api/go.mod api/go.sum ./api/
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o minio-bucket-controller main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine
WORKDIR /
COPY --from=builder /workspace/minio-bucket-controller /usr/local/bin/minio-bucket-controller
USER 65532:65532

ENTRYPOINT ["minio-bucket-controller"]

FROM golang:1.17 AS builder
ARG arch
WORKDIR /src
COPY . /src
RUN CGO_ENABLED=0 GOARCH=${arch} go build -a -ldflags '-s -w -extldflags "-static"' -o /hostpath-provisioner /src

FROM scratch
COPY --from=builder /hostpath-provisioner /hostpath-provisioner
CMD ["/hostpath-provisioner"]

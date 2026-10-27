FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY helper/go.mod ./helper/
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/vcenter-test-profiles ./cmd/vcenter-test-profiles
RUN mkdir -p /out/tmp && chmod 1777 /out/tmp

FROM scratch
COPY --from=builder /out/ /
EXPOSE 443
ENTRYPOINT ["/vcenter-test-profiles"]
CMD ["-listen", "0.0.0.0:443"]

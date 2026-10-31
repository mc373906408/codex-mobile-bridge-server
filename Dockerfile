ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bridge-server ./cmd/bridge-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bridge-server /bridge-server
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/bridge-server"]
CMD ["serve"]

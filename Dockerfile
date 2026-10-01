FROM golang:1.27.1-alpine3.24 AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/todod ./cmd/todod

FROM scratch
COPY --from=builder /out/todod /todod
USER 65532:65532
EXPOSE 7337
ENTRYPOINT ["/todod"]

# Build
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/xenia-log-ingest .

# Run (distroless-ish scratch: static binary, no libc needed with CGO_ENABLED=0)
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/xenia-log-ingest /xenia-log-ingest
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/xenia-log-ingest"]

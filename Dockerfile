# Build: one static binary, no cgo (the SQLite driver is pure Go).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gradebot ./cmd/gradebot

# Run: CA certificates and the binary, as a user without rights (compose sets the owner of data/ instead).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gradebot /gradebot
WORKDIR /app
# Long polling only: no port to expose.
ENTRYPOINT ["/gradebot"]
CMD ["--data", "data"]

# The image holds a binary built elsewhere, so the host compiles nothing:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/gradebot ./cmd/gradebot
# CA certificates come with distroless; the bot runs as the user compose sets (the owner of data/).
FROM gcr.io/distroless/static-debian12:nonroot
COPY bin/gradebot /gradebot
WORKDIR /app
# Long polling only: no port to expose.
ENTRYPOINT ["/gradebot"]
CMD ["--data", "data"]

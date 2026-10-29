# The hosted site: `dsn serve -hosted` in a Cloudflare container.
# Templates and static files are embedded in the binary; media lives in R2.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /dsn ./cmd/dsn

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /dsn /dsn
EXPOSE 8080
ENTRYPOINT ["/dsn", "serve", "-hosted", "-addr", ":8080"]

FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /release-proxy .

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /release-proxy /release-proxy

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/release-proxy"]

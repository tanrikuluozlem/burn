FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /burn ./cmd/burn

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /burn /burn
USER nonroot:nonroot
ENTRYPOINT ["/burn"]

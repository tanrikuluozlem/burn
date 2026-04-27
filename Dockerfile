FROM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG COMMIT=none
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /burn ./cmd/burn

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /burn /burn
USER nonroot:nonroot
ENTRYPOINT ["/burn"]

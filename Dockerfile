# Distroless, per the project conventions — and here the small surface is not
# only hygiene: this image runs on other people's laptops, holding a credential
# that can read and write their boards. There is no shell in it to be handed
# one, and no package manager to fetch anything at runtime.
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cached dependency layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev

# CGO off for a static binary; trimpath and -s -w keep the image small.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/noeto-mcp .

# No HEALTHCHECK and no EXPOSE: this is a stdio server. It has no port and no
# notion of being "up" — the agent host starts it, talks over the pipe, and the
# process ends with the conversation.
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /out/noeto-mcp /usr/local/bin/noeto-mcp

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/noeto-mcp"]

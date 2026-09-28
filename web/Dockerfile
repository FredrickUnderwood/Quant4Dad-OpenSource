# Build context is the repository root; the Web process reads local configuration.
FROM node:24.20.0-alpine AS frontend
ARG NPM_CONFIG_REGISTRY=https://registry.npmjs.org
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web ./
RUN npm run build

FROM golang:1.26.8-alpine3.24 AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/quant4dad-web ./cmd/quant4dad-web

FROM alpine:3.24.1
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/quant4dad-web /app/quant4dad-web
COPY --from=frontend /src/web/dist /app/web
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=5 \
  CMD /app/quant4dad-web --check-ready http://127.0.0.1:8080/readyz
ENTRYPOINT ["/app/quant4dad-web"]

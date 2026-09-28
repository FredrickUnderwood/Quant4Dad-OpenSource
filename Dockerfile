FROM alpine:3.24.1 AS python-runtime
RUN apk add --no-cache curl ca-certificates
COPY scripts/fetch-python-runtime.sh /fetch-python-runtime.sh
RUN sh /fetch-python-runtime.sh /python-wasi

FROM golang:1.26.8-alpine3.24 AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=1
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/quant4dad ./cmd/quant4dad \
    && go build -trimpath -ldflags="-s -w" -o /out/quant4dad-import ./cmd/quant4dad-import

FROM alpine:3.24.1
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /app/data && chmod 1777 /app/data
WORKDIR /app
COPY --from=build /out/quant4dad /app/quant4dad
COPY --from=build /out/quant4dad-import /app/quant4dad-import
COPY --from=python-runtime /python-wasi /opt/q4d/python-wasi
COPY third_party/python-wasi/LICENSE.txt /opt/q4d/python-wasi/LICENSE.txt
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/quant4dad"]

# Stage 1: Build (compilação)
# Usamos uma imagem Go completa para compilar o binário.
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copia código-fonte.
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Compila o binário estático.
# Flags:
#   -o = output file
#   -ldflags = link flags (desabilita debug, reduz tamanho)
#   CGO_ENABLED=0 = sem dependências C (compilação estática pura)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o /tmp/server ./cmd/server

# Stage 2: Runtime (imagem final minimalista)
# Usamos alpine (5 MB) em vez de debian (100 MB+).
FROM alpine:3.20

# Instala CA certificates (para HTTPS em produção, se necessário).
RUN apk add --no-cache ca-certificates wget

WORKDIR /app

# Copia o binário compilado do stage 1.
COPY --from=builder /tmp/server ./server

# Argumentos de build (podem ser sobrescrevidos em docker-compose).
ARG GOGC=300

# Variáveis de ambiente:
#   GOGC=300: garbage collection menos agressivo (melhor performance, tradeoff de memória)
#   GOMEMLIMIT: limite de memória do runtime Go (definida no docker-compose)
ENV GOGC=${GOGC}
ENV RESOURCES_DIR=/app/resources

# Expõe porta 9999.
EXPOSE 9999

# Health check: tenta conectar a GET /ready
HEALTHCHECK --interval=5s --timeout=3s --retries=20 --start-period=30s \
    CMD wget -q -O- http://127.0.0.1:9999/ready || exit 1

# Entry point: executa o servidor.
CMD ["./server"]

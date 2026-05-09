# 🐓 Cocorico — Rinha de Backend 2026 em Go

Implementação competitiva da detecção de fraude via busca vetorial (KNN) para a **Rinha de Backend 2026**.

## 🎯 Objetivo desta participação

Este projeto foi desenvolvido com dois objetivos principais:

- aprender mais sobre a linguagem Go na prática;
- participar da Rinha de Backend 2026 e evoluir tecnicamente com o desafio.

## 📋 Visão Geral

- **Linguagem**: Go 1.24 (performance nativa, sem overhead de VM)
- **Servidor HTTP**: fasthttp (minimalista e ultrarrápido)
- **Load balancer**: Nginx com round-robin
- **Arquitetura**: 2 instâncias API + 1 load balancer
- **Limite de recursos**: 1 CPU + 350 MB (exatamente no limite!)

## 🚀 Como Usar

### Pré-requisitos

- Docker e Docker Compose
- (Opcional) Go 1.24 se quiser compilar localmente

### Build e Run

```bash
# Clonar o repositório
git clone <seu-repo> go-cocorico
cd go-cocorico

# Baixar os arquivos de dados da Rinha (references.json.gz, mcc_risk.json, normalization.json)
# E colocar em ./resources/

# Build com Docker Compose
docker-compose build

# Rodar
docker-compose up
```

Depois, teste a API:

```bash
# Health check
curl http://localhost:9999/ready

# Fraud score (exemplo)
curl -X POST http://localhost:9999/fraud-score \
  -H "Content-Type: application/json" \
  -d '{
    "id": "tx-123",
    "transaction": { "amount": 100.0, "installments": 1, "requested_at": "2026-03-11T20:23:35Z" },
    "customer": { "avg_amount": 200.0, "tx_count_24h": 2, "known_merchants": [] },
    "merchant": { "id": "MERC-001", "mcc": "5411", "avg_amount": 150.0 },
    "terminal": { "is_online": true, "card_present": false, "km_from_home": 10.0 },
    "last_transaction": null
  }'
```

## 📂 Estrutura do Projeto

```
go-cocorico/
├── cmd/
│   └── server/
│       └── main.go                 # Entry point: inicia servidor
├── internal/
│   ├── api/
│   │   └── handler.go              # Handlers HTTP (/ready, /fraud-score)
│   ├── loader/
│   │   └── loader.go               # Carrega references.json.gz, mcc_risk.json, normalization.json
│   ├── model/
│   │   └── types.go                # Estruturas de dados (Request, Response, Index, etc.)
│   ├── search/
│   │   └── knn.go                  # KNN: busca dos 5 vizinhos mais próximos
│   └── vectorizer/
│       └── vectorizer.go           # Vetorização: as 14 dimensões com normalização
├── Dockerfile                       # Multi-stage build: compilação + runtime alpine
├── docker-compose.yml               # Orquestração: 2 apps + nginx load balancer
├── nginx.conf                       # Config do load balancer round-robin
├── go.mod                           # Dependências (fasthttp, goccy/go-json)
├── go.sum                           # Hashes das dependências
└── README.md                        # Este arquivo
```

## 🔧 Tuning de Memória

**docker-compose.yml** aloca:

```yaml
app1:
  memory: 160MB  # 56 MB overhead + ~100 MB para os 3M vetores
  cpus: 0.45

app2:
  memory: 160MB
  cpus: 0.45

nginx:
  memory: 30MB
  cpus: 0.10

Total: 350 MB, 1.00 CPU
```

Os 3M de vetores (3.000.000 × 14 floats × 4 bytes = ~168 MB) compartilham memória entre as duas instâncias via copy-on-write do SO.

## 🧪 Testes Locais

```bash
# Compilar binário local
go build -o bin/server ./cmd/server

# Rodar servidor
RESOURCES_DIR=./resources ./bin/server

# Em outro terminal, testar
curl http://localhost:9999/ready
```

## 📊 Métrica de Sucesso

- **Latência p99**: Alvo < 50 ms
- **Throughput**: Alvo > 500 req/s com 2 CPUs
- **Taxa de erro**: < 0.1%
- **Uso de memória**: Manter em ~320 MB com margem de segurança

## 📖 Referências

- [Rinha de Backend 2026](https://github.com/zanfranceschi/rinha-de-backend-2026)
- [fasthttp docs](https://github.com/valyala/fasthttp)
- [Go performance tips](https://golang.org/doc/effective_go#pointers_vs_values)

## 📝 Licença

MIT

---

**Autor**: Davi Voltas  
**Stack**: Go 1.24, Nginx, Docker, Fasthttp.

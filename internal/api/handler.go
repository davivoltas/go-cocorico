// Package api contém os handlers HTTP para os endpoints da API.
package api

import (
	"bytes"
	"go-cocorico/internal/model"
	"go-cocorico/internal/search"
	"go-cocorico/internal/vectorizer"
	"sync/atomic"

	json "github.com/goccy/go-json"
	"github.com/valyala/fasthttp"
)

// Router é o roteador HTTP que despacha requisições para handlers.
type Router struct {
	idx   *model.Index
	ready *atomic.Bool
}

// NewRouter cria um novo roteador.
func NewRouter(idx *model.Index, ready *atomic.Bool) *Router {
	return &Router{idx: idx, ready: ready}
}

// HandleRequest é o handler principal que processa todas as requisições HTTP.
// Fasthttp passa um contexto para cada requisição (similar a PSR-7 ou Laravel Request).
func (r *Router) HandleRequest(ctx *fasthttp.RequestCtx) {
	path := ctx.Path()

	// Usa byte comparison para performance (evita string allocation).
	switch {
	case bytes.Equal(path, []byte("/fraud-score")):
		if ctx.IsPost() {
			r.handleFraudScore(ctx)
			return
		}
		ctx.SetStatusCode(fasthttp.StatusMethodNotAllowed)
		return
	case bytes.Equal(path, []byte("/ready")):
		r.handleReady(ctx)
		return
	default:
		ctx.SetStatusCode(fasthttp.StatusNotFound)
	}
}

// handleReady implementa GET /ready.
// Retorna 2xx se a API está pronta, 503 caso contrário.
func (r *Router) handleReady(ctx *fasthttp.RequestCtx) {
	if r.ready.Load() {
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyString("ok")
	} else {
		ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
		ctx.SetBodyString("loading")
	}
}

// handleFraudScore implementa POST /fraud-score.
// Fluxo:
//  1. Parse JSON do request body
//  2. Vetoriza o payload em 14 dimensões
//  3. Busca os 5 vizinhos mais próximos (KNN)
//  4. Computa fraud_score e decision
//  5. Retorna resposta JSON
func (r *Router) handleFraudScore(ctx *fasthttp.RequestCtx) {
	// Parse JSON do body.
	var req model.FraudRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"error":"invalid json"}`)
		return
	}

	// Vetoriza o payload em 14 dimensões.
	queryVec, err := vectorizer.Vectorize(&req, r.idx.MCCRisk, r.idx.Normconsts)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"error":"invalid payload"}`)
		return
	}

	// Busca KNN: encontra os 5 vizinhos mais próximos e conta fraudes.
	fraudCount := search.Search(r.idx, &queryVec)

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	if fraudCount < 0 {
		fraudCount = 0
	} else if fraudCount > 5 {
		fraudCount = 5
	}
	ctx.SetBody(r.idx.Responses[fraudCount])
}

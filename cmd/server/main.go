// Package main é o entry point da aplicação.
package main

import (
	"go-cocorico/internal/api"
	"go-cocorico/internal/loader"
	"log"
	"os"
	"sync/atomic"

	"github.com/valyala/fasthttp"
)

func main() {
	// Caminho para a pasta de recursos (references.json.gz, mcc_risk.json, etc.)
	resourcesDir := os.Getenv("RESOURCES_DIR")
	if resourcesDir == "" {
		resourcesDir = "/app/resources"
	}

	// Carrega todos os dados em memória durante startup.
	// Isso pode levar alguns segundos com 3 milhões de vetores.
	log.Printf("Loading index from %s...", resourcesDir)
	idx, err := loader.LoadIndex(resourcesDir)
	if err != nil {
		log.Fatalf("Failed to load index: %v", err)
	}
	log.Printf("Index loaded: %d references, %d MCC entries", len(idx.Refs), len(idx.MCCRisk))

	// Flag para marcar quando a API está pronta para aceitar requisições.
	ready := &atomic.Bool{}
	ready.Store(true)

	// Cria o roteador da API.
	router := api.NewRouter(idx, ready)

	// Inicia servidor fasthttp na porta 9999.
	// fasthttp é um servidor HTTP minimalista e muito rápido (usado por grandes players como Cloudflare).
	port := ":9999"
	log.Printf("Server listening on %s", port)

	// O servidor bloqueia nesta linha. Se fechar, a aplicação encerra.
	if err := fasthttp.ListenAndServe(port, router.HandleRequest); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

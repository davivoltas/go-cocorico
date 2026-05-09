// Package model contém as estruturas de dados.
package model

// RefEntry é um vetor de referência (13 float32 + 1 bool = 60 bytes).
// Em Go, quando usamos array fixo [14]float32, o compilador pode otimizar com SIMD.
// Isso é muito mais rápido que um slice []float32 porque o tamanho é conhecido.
type RefEntry struct {
	V       [14]float32 // As 14 dimensões do vetor
	IsFraud bool        // Rótulo: true = fraude, false = legítimo
	_       [3]byte     // Padding para cache-line (otimização de performance)
}

// FraudRequest é o payload recebido no POST /fraud-score.
// json tags informam ao decoder JSON como mapear os campos.
type FraudRequest struct {
	ID          string        `json:"id"`
	Transaction TxInput       `json:"transaction"`
	Customer    CustomerInput `json:"customer"`
	Merchant    MerchantInput `json:"merchant"`
	Terminal    TerminalInput `json:"terminal"`
	LastTx      *LastTxInput  `json:"last_transaction"` // Pode ser null, por isso é *pointer
}

// TxInput contém dados da transação.
type TxInput struct {
	Amount       float64 `json:"amount"`
	Installments int     `json:"installments"`
	RequestedAt  string  `json:"requested_at"` // RFC3339: "2026-03-11T20:23:35Z"
}

// CustomerInput contém dados do cliente.
type CustomerInput struct {
	AvgAmount      float64  `json:"avg_amount"`
	TxCount24h     int      `json:"tx_count_24h"`
	KnownMerchants []string `json:"known_merchants"`
}

// MerchantInput contém dados do comerciante.
type MerchantInput struct {
	ID        string  `json:"id"`
	MCC       string  `json:"mcc"` // Merchant Category Code
	AvgAmount float64 `json:"avg_amount"`
}

// TerminalInput contém dados do terminal.
type TerminalInput struct {
	IsOnline    bool    `json:"is_online"`
	CardPresent bool    `json:"card_present"`
	KmFromHome  float64 `json:"km_from_home"`
}

// LastTxInput contém dados da transação anterior (pode ser null).
type LastTxInput struct {
	Timestamp     string  `json:"timestamp"` // RFC3339
	KmFromCurrent float64 `json:"km_from_current"`
}

// FraudResponse é a resposta JSON do endpoint /fraud-score.
type FraudResponse struct {
	Approved   bool    `json:"approved"`
	FraudScore float32 `json:"fraud_score"`
}

// Index contém todos os dados carregados em memória durante startup.
type Index struct {
	Refs           []RefEntry              // Todos os 3 milhões de vetores de referência (pode ser nil se usar streaming)
	ReferencesFile string                  // Path para references.json.gz quando usando streaming
	MCCRisk        map[string]float32      // MCC -> risco (lookup table)
	Normconsts     *NormalizationConstants // Constantes de normalização
	Responses      [6][]byte               // Respostas JSON pré-computadas por fraudCount (0..5)
	Buckets        map[uint32][]int        // Índice aproximado: bucket -> posições em Refs
	Shards         [][]RefEntry            // Views fatiadas de Refs para busca KNN paralela
}

// NormalizationConstants contém as constantes usadas nas fórmulas de normalização.
// Carregadas do arquivo normalization.json.
type NormalizationConstants struct {
	MaxAmount            float64 `json:"max_amount"`
	MaxInstallments      float64 `json:"max_installments"`
	AmountVsAvgRatio     float64 `json:"amount_vs_avg_ratio"`
	MaxMinutes           float64 `json:"max_minutes"`
	MaxKm                float64 `json:"max_km"`
	MaxTxCount24h        float64 `json:"max_tx_count_24h"`
	MaxMerchantAvgAmount float64 `json:"max_merchant_avg_amount"`
}

// ReferenceData é a estrutura para desserializar references.json.gz
// Cada linha é um objeto JSON com 'V' (array de 14 floats) e 'Label' (string).
type ReferenceData struct {
	V     [14]float32 `json:"V"`
	Label string      `json:"Label"`
}

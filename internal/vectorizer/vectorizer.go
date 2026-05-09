// Package vectorizer contém a lógica de transformação de payload em vetor.
// Este é o coração da normalização: as 14 dimensões com suas fórmulas.
package vectorizer

import (
	"fmt"
	"go-cocorico/internal/model"
	"time"
)

// Clamp limita um valor ao intervalo [0, 1].
func clamp(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Vectorize transforma um FraudRequest em um vetor de 14 dimensões.
// Retorna o vetor ou um erro se o payload tiver dados inválidos.
//
// As 14 dimensões, nesta ordem:
//  0. amount (normalized by max_amount)
//  1. installments (normalized by max_installments)
//  2. amount_vs_avg (transaction.amount / customer.avg_amount, normalized)
//  3. hour_of_day (0-23 from requested_at, divided by 23)
//  4. day_of_week (Monday=0..Sunday=6, divided by 6)
//  5. minutes_since_last_tx (-1 if no last_transaction, else normalized)
//  6. km_from_last_tx (-1 if no last_transaction, else normalized)
//  7. km_from_home
//  8. tx_count_24h
//  9. is_online (1 or 0)
//
// 10. card_present (1 or 0)
// 11. unknown_merchant (1 if merchant not in known_merchants, else 0)
// 12. mcc_risk (from mcc_risk.json, default 0.5)
// 13. merchant_avg_amount
func Vectorize(
	req *model.FraudRequest,
	mccRisk map[string]float32,
	normconsts *model.NormalizationConstants,
) ([14]float32, error) {
	var v [14]float32

	// Parse timestamp da requisição para extrair hora e dia da semana.
	reqAt, err := time.Parse(time.RFC3339, req.Transaction.RequestedAt)
	if err != nil {
		return v, fmt.Errorf("invalid requested_at: %w", err)
	}

	// Dimensão 0: amount (normalized)
	v[0] = clamp(float32(req.Transaction.Amount) / float32(normconsts.MaxAmount))

	// Dimensão 1: installments (normalized)
	v[1] = clamp(float32(req.Transaction.Installments) / float32(normconsts.MaxInstallments))

	// Dimensão 2: amount_vs_avg
	// Fórmula: (transaction.amount / customer.avg_amount) / amount_vs_avg_ratio
	// Se avg_amount for 0, assume ratio muito alto (1.0).
	if req.Customer.AvgAmount > 0 {
		ratio := (req.Transaction.Amount / req.Customer.AvgAmount) / normconsts.AmountVsAvgRatio
		v[2] = clamp(float32(ratio))
	} else {
		v[2] = 1.0
	}

	// Dimensão 3: hour_of_day (0-23, normalized para 0-1)
	v[3] = float32(reqAt.Hour()) / 23.0

	// Dimensão 4: day_of_week (Monday=0..Sunday=6, normalized para 0-1)
	// Go's time.Weekday: Sunday=0, Monday=1, ..., Saturday=6
	// Rinha especifica: Monday=0, Tuesday=1, ..., Sunday=6
	// Conversão: (weekday_go + 6) % 7 => monday=0..sunday=6
	weekdayRinha := (int(reqAt.Weekday()) + 6) % 7
	v[4] = float32(weekdayRinha) / 6.0

	// Dimensões 5 e 6: dados da transação anterior (se existir)
	// Se last_transaction for null, ambas recebem -1 como valor sentinela.
	if req.LastTx != nil {
		lastAt, err := time.Parse(time.RFC3339, req.LastTx.Timestamp)
		if err != nil {
			// Se timestamp da transação anterior for inválido, usa -1.
			v[5] = -1
			v[6] = -1
		} else {
			// Dimensão 5: minutos desde a última transação
			minutesSince := reqAt.Sub(lastAt).Minutes()
			v[5] = clamp(float32(minutesSince) / float32(normconsts.MaxMinutes))

			// Dimensão 6: distância desde a última transação
			v[6] = clamp(float32(req.LastTx.KmFromCurrent) / float32(normconsts.MaxKm))
		}
	} else {
		v[5] = -1
		v[6] = -1
	}

	// Dimensão 7: km_from_home (distância do endereço)
	v[7] = clamp(float32(req.Terminal.KmFromHome) / float32(normconsts.MaxKm))

	// Dimensão 8: tx_count_24h (transações nas últimas 24h)
	v[8] = clamp(float32(req.Customer.TxCount24h) / float32(normconsts.MaxTxCount24h))

	// Dimensão 9: is_online (booleano: 1 se online, 0 senão)
	if req.Terminal.IsOnline {
		v[9] = 1.0
	} else {
		v[9] = 0.0
	}

	// Dimensão 10: card_present (booleano: 1 se presente, 0 senão)
	if req.Terminal.CardPresent {
		v[10] = 1.0
	} else {
		v[10] = 0.0
	}

	// Dimensão 11: unknown_merchant
	// 1 se o merchant NÃO está em customer.known_merchants, senão 0.
	isUnknown := float32(1.0)
	for _, known := range req.Customer.KnownMerchants {
		if known == req.Merchant.ID {
			isUnknown = 0.0
			break
		}
	}
	v[11] = isUnknown

	// Dimensão 12: mcc_risk (lookup na tabela, default 0.5)
	mccVal, ok := mccRisk[req.Merchant.MCC]
	if !ok {
		mccVal = 0.5
	}
	v[12] = mccVal

	// Dimensão 13: merchant_avg_amount
	v[13] = clamp(float32(req.Merchant.AvgAmount) / float32(normconsts.MaxMerchantAvgAmount))

	return v, nil
}

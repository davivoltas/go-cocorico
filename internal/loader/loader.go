// Package loader contém a lógica para carregar dados dos arquivos de referência.
// Os dados são carregados uma única vez no startup e mantidos em memória durante toda a vida da aplicação.
package loader

import (
	"compress/gzip"
	"fmt"
	"go-cocorico/internal/model"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
)

// LoadIndex carrega o índice completo: vetores de referência, MCC risk e constantes.
func LoadIndex(resourcesDir string) (*model.Index, error) {
	idx := &model.Index{
		MCCRisk: make(map[string]float32),
	}

	// Carrega constantes de normalização.
	normconsts, err := loadNormalization(filepath.Join(resourcesDir, "normalization.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to load normalization.json: %w", err)
	}
	idx.Normconsts = normconsts

	// Carrega MCC risk map.
	mccRisk, err := loadMCCRisk(filepath.Join(resourcesDir, "mcc_risk.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to load mcc_risk.json: %w", err)
	}
	idx.MCCRisk = mccRisk

	// Preload parcial com teto para manter o caminho quente em memória sem estourar RAM.
	refsPath := filepath.Join(resourcesDir, "references.json.gz")
	// Interpret REF_PRELOAD_MAX as:
	//  - unset: default 8000
	//  - >0: preload that many entries
	//  - <=0: load ALL entries (no cap)
	maxRefs := 8000
	if raw := os.Getenv("REF_PRELOAD_MAX"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			if v <= 0 {
				maxRefs = math.MaxInt32
			} else {
				maxRefs = v
			}
		}
	}
	// O caminho quente do benchmark fica muito mais rápido quando todo o dataset
	// de referência já está em memória.
	refs, err := loadReferences(refsPath, maxRefs)
	if err != nil {
		return nil, fmt.Errorf("failed to load references.json.gz: %w", err)
	}
	idx.Refs = refs
	idx.Buckets = buildBuckets(refs)

	// Support optional parallel KNN by slicing Refs into Shards.
	// Number of shards controlled by env `KNN_WORKERS` (default 1).
	numShards := envIntOrDefault("KNN_WORKERS", 1)
	if numShards > 1 && len(idx.Refs) > 0 {
		shardSize := (len(idx.Refs) + numShards - 1) / numShards
		idx.Shards = make([][]model.RefEntry, numShards)
		for i := 0; i < numShards; i++ {
			start := i * shardSize
			end := start + shardSize
			if end > len(idx.Refs) {
				end = len(idx.Refs)
			}
			idx.Shards[i] = idx.Refs[start:end]
		}
	}

	precomputeResponses(idx)

	return idx, nil
}

// loadNormalization lê o arquivo normalization.json.
func loadNormalization(filepath string) (*model.NormalizationConstants, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	var nc model.NormalizationConstants
	if err := json.Unmarshal(data, &nc); err != nil {
		return nil, err
	}

	return &nc, nil
}

// loadMCCRisk lê o arquivo mcc_risk.json.
// Formato: { "5411": 0.1, "7802": 0.8, ... }
func loadMCCRisk(filepath string) (map[string]float32, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	risk := make(map[string]float32)
	if err := json.Unmarshal(data, &risk); err != nil {
		return nil, err
	}

	return risk, nil
}

// loadReferences lê references.json.gz em streaming e suporta tanto:
// 1) array JSON com campos `vector`/`label`
// 2) sequência de objetos JSON com `V`/`Label`.
func loadReferences(filepath string, maxEntries int) ([]model.RefEntry, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Abre gzip reader.
	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	dec := json.NewDecoder(gz)

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}

	type referenceDataFlexible struct {
		V          [14]float32 `json:"V"`
		Vector     [14]float32 `json:"vector"`
		Label      string      `json:"Label"`
		LabelLower string      `json:"label"`
	}

	refs := make([]model.RefEntry, 0, 100_000)
	appendEntry := func(rd referenceDataFlexible) {
		label := rd.Label
		if label == "" {
			label = rd.LabelLower
		}

		vec := rd.V
		if rd.Vector != ([14]float32{}) {
			vec = rd.Vector
		}

		refs = append(refs, model.RefEntry{
			V:       vec,
			IsFraud: strings.EqualFold(label, "fraud"),
		})
	}

	if d, ok := tok.(json.Delim); ok && d == '[' {
		var rd referenceDataFlexible
		for dec.More() {
			if len(refs) >= maxEntries {
				break
			}
			if err := dec.Decode(&rd); err != nil {
				return nil, err
			}
			appendEntry(rd)
		}

		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return refs, nil
	}

	// Fallback para stream de objetos JSON (JSONL sem quebra obrigatória).
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	if err := gz.Reset(file); err != nil {
		return nil, err
	}
	dec = json.NewDecoder(gz)

	var rd referenceDataFlexible
	for {
		if len(refs) >= maxEntries {
			break
		}
		if err := dec.Decode(&rd); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		appendEntry(rd)
	}

	return refs, nil
}

func envIntOrDefault(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}

	return v
}

func precomputeResponses(idx *model.Index) {
	denyFrom := envIntOrDefault("FRAUD_DENY_FROM", 3)
	if denyFrom < 0 {
		denyFrom = 0
	}
	if denyFrom > 5 {
		denyFrom = 5
	}

	for i := 0; i <= 5; i++ {
		approved := i < denyFrom
		score := float32(i) * 0.2
		if approved {
			idx.Responses[i] = []byte(fmt.Sprintf(`{"approved":true,"fraud_score":%.1f}`, score))
		} else {
			idx.Responses[i] = []byte(fmt.Sprintf(`{"approved":false,"fraud_score":%.1f}`, score))
		}
	}
	idx.ReferencesFile = ""
}

func buildBuckets(refs []model.RefEntry) map[uint32][]int {
	if len(refs) == 0 {
		return nil
	}

	buckets := make(map[uint32][]int, 4096)
	for i := range refs {
		key := bucketKey(&refs[i].V)
		buckets[key] = append(buckets[key], i)
	}
	return buckets
}

func bucketKey(v *[14]float32) uint32 {
	const bins = 16
	b0 := quantize(v[0], bins)
	b1 := quantize(v[2], bins)
	b2 := quantize(v[12], bins)
	return uint32(b0) | (uint32(b1) << 8) | (uint32(b2) << 16)
}

func quantize(v float32, bins int) int {
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	q := int(v * float32(bins-1))
	if q < 0 {
		return 0
	}
	if q >= bins {
		return bins - 1
	}
	return q
}

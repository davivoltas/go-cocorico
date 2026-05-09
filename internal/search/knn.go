// Package search contém a lógica de busca vetorial K-Nearest Neighbors.
// KNN é a base da detecção de fraude: encontra os 5 vizinhos mais próximos
// e conta quantos deles são fraudulentos.
package search

import (
	"bufio"
	"compress/gzip"
	"go-cocorico/internal/model"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	json "github.com/goccy/go-json"
)

const K = 5 // Procuramos sempre os 5 vizinhos mais próximos

const (
	incrementalInitialLoad = 20000
	incrementalLoadChunk   = 5000
	defaultCacheMaxEntries = 200000
)

var cacheMaxEntries = envIntOrDefault("REF_CACHE_MAX_ENTRIES", defaultCacheMaxEntries)

type referenceCache struct {
	mu sync.Mutex

	refs []model.RefEntry

	stream *referenceStream

	fullyScanned  bool
	growthStopped bool
	initErr       error
}

var referenceCaches sync.Map

type referenceStream struct {
	file    *os.File
	gz      *gzip.Reader
	reader  *bufio.Reader
	decoder *json.Decoder
	inArray bool
	closed  bool
}

type referenceDataFlexible struct {
	V          [14]float32 `json:"V"`
	Vector     [14]float32 `json:"vector"`
	Label      string      `json:"Label"`
	LabelLower string      `json:"label"`
}

// neighbor representa um vizinho encontrado com sua distância e rótulo.
type neighbor struct {
	distSq  float32 // Distância euclidiana ao quadrado (não precisa sqrt para ordenação)
	isFraud bool
}

// SquaredEuclideanDist calcula a distância euclidiana ao quadrado entre dois vetores.
// Usamos ao quadrado porque:
//  1. Não precisa sqrt (mais rápido)
//  2. A ordenação é preservada (se dist1² < dist2², então dist1 < dist2)
func squaredEuclideanDist(a, b *[14]float32) float32 {
	var sum float32
	for i := 0; i < 14; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// Search realiza a busca KNN: encontra os 5 vizinhos mais próximos
// e retorna a quantidade de fraudes entre eles.
func Search(idx *model.Index, query *[14]float32) int {
	// Se os refs estiverem carregados em memória, usa o caminho rápido.
	if len(idx.Refs) > 0 {
		// If shards are available, prefer parallel KNN.
		if len(idx.Shards) > 1 {
			return knnParallel(idx.Shards, query)
		}
		if len(idx.Buckets) > 0 {
			return searchBucketed(idx, query)
		}
		return searchSequential(idx.Refs, query)
	}

	// Fallback defensivo para cenários sem preload completo.
	if idx.ReferencesFile != "" {
		return searchStreamingFromFile(idx.ReferencesFile, query)
	}

	// Sem referências disponíveis, retorna 0 fraudes por segurança.
	return 0
}

func searchBucketed(idx *model.Index, query *[14]float32) int {
	const bins = 16
	center0 := quantize(query[0], bins)
	center1 := quantize(query[2], bins)
	center2 := quantize(query[12], bins)

	var top [K]neighbor
	for i := range top {
		top[i].distSq = math.MaxFloat32
	}

	key := uint32(center0) | (uint32(center1) << 8) | (uint32(center2) << 16)
	ids := idx.Buckets[key]
	for _, id := range ids {
		entry := &idx.Refs[id]
		distSq := squaredEuclideanDist(query, &entry.V)
		maxIdx, maxDist := farthestNeighbor(top)
		if distSq < maxDist {
			top[maxIdx] = neighbor{distSq: distSq, isFraud: entry.IsFraud}
		}
	}

	fraudCount := 0
	for i := range top {
		if top[i].isFraud {
			fraudCount++
		}
	}
	return fraudCount
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

func getReferenceCache(path string) *referenceCache {
	if c, ok := referenceCaches.Load(path); ok {
		return c.(*referenceCache)
	}

	newCache := &referenceCache{}
	actual, _ := referenceCaches.LoadOrStore(path, newCache)
	return actual.(*referenceCache)
}

func (c *referenceCache) loadIncremental(path string) []model.RefEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.initErr == nil && !c.fullyScanned && !c.growthStopped {
		toLoad := incrementalLoadChunk
		if len(c.refs) == 0 {
			toLoad = incrementalInitialLoad
		}
		c.loadEntriesLocked(path, toLoad)
	}

	return c.refs
}

func (c *referenceCache) loadEntriesLocked(path string, toLoad int) {
	if len(c.refs) >= cacheMaxEntries {
		c.growthStopped = true
		c.closeReadersLocked()
		return
	}

	if err := c.ensureStreamLocked(path); err != nil {
		c.initErr = err
		c.closeReadersLocked()
		return
	}

	loaded := 0
	for loaded < toLoad && len(c.refs) < cacheMaxEntries {
		entry, err := c.stream.nextEntry()
		if err == io.EOF {
			c.fullyScanned = true
			c.closeReadersLocked()
			return
		}
		if err != nil {
			c.initErr = err
			c.fullyScanned = true
			c.closeReadersLocked()
			return
		}

		c.refs = append(c.refs, entry)
		loaded++
	}

	if len(c.refs) >= cacheMaxEntries {
		c.growthStopped = true
		c.closeReadersLocked()
	}
}

func (c *referenceCache) ensureStreamLocked(path string) error {
	if c.stream != nil {
		return nil
	}

	stream, err := newReferenceStream(path)
	if err != nil {
		return err
	}

	c.stream = stream
	return nil
}

func newReferenceStream(path string) (*referenceStream, error) {

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	reader := bufio.NewReader(gz)
	first, err := peekFirstNonSpace(reader)
	if err != nil {
		_ = gz.Close()
		_ = file.Close()
		return nil, err
	}

	decoder := json.NewDecoder(reader)
	stream := &referenceStream{
		file:    file,
		gz:      gz,
		reader:  reader,
		decoder: decoder,
		inArray: first == '[',
	}

	if stream.inArray {
		tok, err := stream.decoder.Token()
		if err != nil {
			stream.close()
			return nil, err
		}
		d, ok := tok.(json.Delim)
		if !ok || d != '[' {
			stream.close()
			return nil, io.ErrUnexpectedEOF
		}
	}

	return stream, nil
}

func peekFirstNonSpace(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.Peek(1)
		if err != nil {
			return 0, err
		}

		switch b[0] {
		case ' ', '\n', '\r', '\t':
			_, _ = r.ReadByte()
			continue
		default:
			return b[0], nil
		}
	}
}

func (s *referenceStream) nextEntry() (model.RefEntry, error) {
	if s.closed {
		return model.RefEntry{}, io.EOF
	}

	if s.inArray && !s.decoder.More() {
		_, err := s.decoder.Token()
		if err != nil && err != io.EOF {
			return model.RefEntry{}, err
		}
		return model.RefEntry{}, io.EOF
	}

	var rd referenceDataFlexible
	if err := s.decoder.Decode(&rd); err != nil {
		return model.RefEntry{}, err
	}

	label := rd.Label
	if label == "" {
		label = rd.LabelLower
	}

	vec := rd.V
	if rd.Vector != ([14]float32{}) {
		vec = rd.Vector
	}

	return model.RefEntry{
		V:       vec,
		IsFraud: strings.EqualFold(label, "fraud"),
	}, nil
}

func (s *referenceStream) close() {
	if s.closed {
		return
	}
	s.closed = true

	if s.gz != nil {
		_ = s.gz.Close()
		s.gz = nil
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

func (c *referenceCache) closeReadersLocked() {
	if c.stream != nil {
		c.stream.close()
		c.stream = nil
	}
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

// searchSequential faz scanning linear de todos os vetores para encontrar os K mais próximos.
// Otimizações:
// 1. Stack-allocated top[K] (sem alocações no heap)
// 2. Tracking de maxDist para early termination em comparações
// 3. O compilador Go otimiza loops com arrays fixos usando SIMD
func searchSequential(refs []model.RefEntry, query *[14]float32) int {
	// top[i] representa o i-ésimo vizinho mais próximo encontrado até agora.
	var top [K]neighbor
	for i := range top {
		top[i].distSq = math.MaxFloat32 // Inicializa com infinito
	}

	// Scan linear de todos os vetores.
	for i := range refs {
		// Calcula distância entre query e refs[i].V
		distSq := squaredEuclideanDist(query, &refs[i].V)

		// Se essa distância é menor que a pior distância nos K top, substitui.
		maxIdx, maxDist := farthestNeighbor(top)
		if distSq < maxDist {
			top[maxIdx] = neighbor{distSq: distSq, isFraud: refs[i].IsFraud}
		}
	}

	// Conta quantos dos K vizinhos são fraudes.
	fraudCount := 0
	for i := range top {
		if top[i].isFraud {
			fraudCount++
		}
	}

	return fraudCount
}

// searchShard scans refs and returns the local top-K neighbors (no heap allocs).
func searchShard(refs []model.RefEntry, query *[14]float32) [K]neighbor {
	var top [K]neighbor
	for i := range top {
		top[i].distSq = math.MaxFloat32
	}

	for i := range refs {
		distSq := squaredEuclideanDist(query, &refs[i].V)
		maxIdx, maxDist := farthestNeighbor(top)
		if distSq < maxDist {
			top[maxIdx] = neighbor{distSq: distSq, isFraud: refs[i].IsFraud}
		}
	}
	return top
}

// knnParallel scans each shard in its own goroutine and merges the results.
func knnParallel(shards [][]model.RefEntry, query *[14]float32) int {
	tops := make([][K]neighbor, len(shards))

	var wg sync.WaitGroup
	wg.Add(len(shards))
	for i, shard := range shards {
		go func(i int, shard []model.RefEntry) {
			tops[i] = searchShard(shard, query)
			wg.Done()
		}(i, shard)
	}
	wg.Wait()

	// merge shard results into a single global top-K
	var merged [K]neighbor
	for i := range merged {
		merged[i].distSq = math.MaxFloat32
	}

	for _, top := range tops {
		for _, n := range top {
			maxIdx, maxDist := farthestNeighbor(merged)
			if n.distSq < maxDist {
				merged[maxIdx] = n
			}
		}
	}
	// count frauds
	fraud := 0
	for i := range merged {
		if merged[i].isFraud {
			fraud++
		}
	}
	return fraud
}

// searchStreamingFromFile percorre o arquivo gzip JSONL e calcula os K vizinhos
// encontrando-os em uma única passagem sem carregar todo o dataset em memória.
func searchStreamingFromFile(path string, query *[14]float32) int {
	stream, err := newReferenceStream(path)
	if err != nil {
		return 0
	}
	defer stream.close()

	var top [K]neighbor
	for i := range top {
		top[i].distSq = math.MaxFloat32
	}

	for {
		entry, err := stream.nextEntry()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		distSq := squaredEuclideanDist(query, &entry.V)
		maxIdx, maxDist := farthestNeighbor(top)
		if distSq < maxDist {
			top[maxIdx] = neighbor{distSq: distSq, isFraud: entry.IsFraud}
		}
	}

	fraudCount := 0
	for i := range top {
		if top[i].isFraud {
			fraudCount++
		}
	}

	return fraudCount
}

// ComputeFraudScore converte a contagem de fraudes em um score (0.0 a 1.0).
// fraud_score = number_of_frauds / K
func ComputeFraudScore(fraudCount int) float32 {
	return float32(fraudCount) / float32(K)
}

func farthestNeighbor(top [K]neighbor) (int, float32) {
	maxIdx := 0
	maxDist := top[0].distSq
	for i := 1; i < K; i++ {
		if top[i].distSq > maxDist {
			maxDist = top[i].distSq
			maxIdx = i
		}
	}
	return maxIdx, maxDist
}

// ShouldApprove decide se a transação deve ser aprovada baseado no fraud_score.
// A regra é simples: approved = fraud_score < 0.6
// (Significa: menos de 60% dos vizinhos são fraudulentos)
func ShouldApprove(fraudScore float32) bool {
	return fraudScore < 0.6
}

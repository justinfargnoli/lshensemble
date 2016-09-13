package lshensemble

import (
	"math"
	"sort"
	"sync"
)

const (
	integrationPrecision = 0.01
)

// Default constructor uses 32 bit hash value
var NewLshForest = NewLshForest32

type keys []string

// For initial bootstrapping
type initHashTable map[string]keys

type bucket struct {
	hashKey string
	keys    keys
}

type hashTable []bucket

func (h hashTable) Len() int           { return len(h) }
func (h hashTable) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h hashTable) Less(i, j int) bool { return h[i].hashKey < h[j].hashKey }

type LshForest struct {
	k              int
	l              int
	initHashTables []initHashTable
	hashTables     []hashTable
	hashKeyFunc    hashKeyFunc
	hashValueSize  int
}

func newLshForest(k, l, hashValueSize int) *LshForest {
	if k < 0 || l < 0 {
		panic("k and l must be positive")
	}
	hashTables := make([]hashTable, l)
	for i := range hashTables {
		hashTables[i] = make(hashTable, 0)
	}
	initHashTables := make([]initHashTable, l)
	for i := range initHashTables {
		initHashTables[i] = make(initHashTable)
	}
	return &LshForest{
		k:              k,
		l:              l,
		hashValueSize:  hashValueSize,
		initHashTables: initHashTables,
		hashTables:     hashTables,
		hashKeyFunc:    hashKeyFuncGen(hashValueSize),
	}
}

func NewLshForest64(k, l int) *LshForest {
	return newLshForest(k, l, 8)
}

func NewLshForest32(k, l int) *LshForest {
	return newLshForest(k, l, 4)
}

func NewLshForest16(k, l int) *LshForest {
	return newLshForest(k, l, 2)
}

// Add a key with signature into the index.
// The key won't be searchable untile Index is called.
func (f *LshForest) Add(key string, sig Signature) {
	// Generate hash keys
	Hs := make([]string, f.l)
	for i := 0; i < f.l; i++ {
		Hs[i] = f.hashKeyFunc(sig[i*f.k : (i+1)*f.k])
	}
	// Insert keys into the bootstrapping tables
	var wg sync.WaitGroup
	wg.Add(len(f.initHashTables))
	for i := range f.initHashTables {
		go func(ht initHashTable, hk, key string) {
			if _, exist := ht[hk]; exist {
				ht[hk] = append(ht[hk], key)
			} else {
				ht[hk] = make(keys, 1)
				ht[hk][0] = key
			}
			wg.Done()
		}(f.initHashTables[i], Hs[i], key)
	}
	wg.Wait()
}

// Make all the keys added searchable.
func (f *LshForest) Index() {
	var wg sync.WaitGroup
	wg.Add(len(f.hashTables))
	for i := range f.hashTables {
		go func(htPtr *hashTable, initHtPtr *initHashTable) {
			// Build sorted hash table using buckets from init hash tables
			initHt := *initHtPtr
			ht := *htPtr
			for hashKey := range initHt {
				ks, _ := initHt[hashKey]
				ht = append(ht, bucket{
					hashKey: hashKey,
					keys:    ks,
				})
			}
			sort.Sort(ht)
			*htPtr = ht
			// Reset the init hash tables
			*initHtPtr = make(initHashTable)
			wg.Done()
		}(&(f.hashTables[i]), &(f.initHashTables[i]))
	}
	wg.Wait()
}

func (f *LshForest) Query(sig Signature, prefixLen, numTables int, out chan string) {
	if prefixLen == -1 {
		prefixLen = f.k
	}
	if numTables == -1 {
		numTables = f.l
	}
	prefixSize := f.hashValueSize * prefixLen
	// Generate hash keys
	Hs := make([]string, numTables)
	for i := 0; i < numTables; i++ {
		Hs[i] = f.hashKeyFunc(sig[i*f.k : i*f.k+prefixLen])
	}
	// Query hash tables in parallel
	keyChan := make(chan string)
	var wg sync.WaitGroup
	wg.Add(numTables)
	for i := 0; i < numTables; i++ {
		go func(ht hashTable, hk string) {
			k := sort.Search(len(ht), func(x int) bool {
				return ht[x].hashKey[:prefixSize] >= hk
			})
			if k < len(ht) && ht[k].hashKey[:prefixSize] == hk {
				for j := k; j < len(ht) && ht[j].hashKey[:prefixSize] == hk; j++ {
					for _, key := range ht[j].keys {
						keyChan <- key
					}
				}
			}
			wg.Done()
		}(f.hashTables[i], Hs[i])
	}
	go func() {
		wg.Wait()
		close(keyChan)
	}()
	seens := make(map[string]bool)
	for key := range keyChan {
		if _, seen := seens[key]; seen {
			continue
		}
		out <- key
		seens[key] = true
	}
}

// OptimalKL returns the optimal k and l for containment search
// where x is the indexed domain size, q is the query domain size,
// and t is the containment threshold.
func (f *LshForest) OptimalKL(x, q int, t float64) (optK, optL int, fp, fn float64) {
	minError := math.MaxFloat64
	for l := 1; l <= f.l; l++ {
		for k := 1; k <= f.k; k++ {
			currFp := probFalsePositive(x, q, l, k, t, integrationPrecision)
			currFn := probFalseNegative(x, q, l, k, t, integrationPrecision)
			currErr := currFn + currFp
			if minError > currErr {
				minError = currErr
				optK = k
				optL = l
				fp = currFp
				fn = currFn
			}
		}
	}
	return
}

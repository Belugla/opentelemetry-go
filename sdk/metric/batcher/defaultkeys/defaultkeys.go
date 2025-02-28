// Copyright 2019, OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package defaultkeys // import "go.opentelemetry.io/otel/sdk/metric/batcher/defaultkeys"

import (
	"context"

	"go.opentelemetry.io/otel/api/core"
	export "go.opentelemetry.io/otel/sdk/export/metric"
)

type (
	Batcher struct {
		selector      export.AggregationSelector
		labelEncoder  export.LabelEncoder
		stateful      bool
		descKeyIndex  descKeyIndexMap
		aggCheckpoint aggCheckpointMap
	}

	// descKeyIndexMap is a mapping, for each Descriptor, from the
	// Key to the position in the descriptor's recommended keys.
	descKeyIndexMap map[*export.Descriptor]map[core.Key]int

	// aggCheckpointMap is a mapping from encoded label set to current
	// export record.  If the batcher is stateful, this map is
	// never cleared.
	aggCheckpointMap map[string]export.Record

	checkpointSet struct {
		aggCheckpointMap aggCheckpointMap
		labelEncoder     export.LabelEncoder
	}
)

var _ export.Batcher = &Batcher{}
var _ export.CheckpointSet = &checkpointSet{}

func New(selector export.AggregationSelector, labelEncoder export.LabelEncoder, stateful bool) *Batcher {
	return &Batcher{
		selector:      selector,
		labelEncoder:  labelEncoder,
		descKeyIndex:  descKeyIndexMap{},
		aggCheckpoint: aggCheckpointMap{},
		stateful:      stateful,
	}
}

func (b *Batcher) AggregatorFor(descriptor *export.Descriptor) export.Aggregator {
	return b.selector.AggregatorFor(descriptor)
}

func (b *Batcher) Process(_ context.Context, record export.Record) error {
	desc := record.Descriptor()
	keys := desc.Keys()

	// Cache the mapping from Descriptor->Key->Index
	ki, ok := b.descKeyIndex[desc]
	if !ok {
		ki = map[core.Key]int{}
		b.descKeyIndex[desc] = ki

		for i, k := range keys {
			ki[k] = i
		}
	}

	// Compute the value list.  Note: Unspecified values become
	// empty strings.  TODO: pin this down, we have no appropriate
	// Value constructor.
	outputLabels := make([]core.KeyValue, len(keys))

	for i, key := range keys {
		outputLabels[i] = key.String("")
	}

	// Note also the possibility to speed this computation of
	// "encoded" via "outputLabels" in the form of a (Descriptor,
	// LabelSet)->(Labels, Encoded) cache.
	for _, kv := range record.Labels().Ordered() {
		pos, ok := ki[kv.Key]
		if !ok {
			continue
		}
		outputLabels[pos].Value = kv.Value
	}

	// Compute an encoded lookup key.
	encoded := b.labelEncoder.Encode(outputLabels)

	// Merge this aggregator with all preceding aggregators that
	// map to the same set of `outputLabels` labels.
	agg := record.Aggregator()
	rag, ok := b.aggCheckpoint[encoded]
	if ok {
		return rag.Aggregator().Merge(agg, desc)
	}
	// If this Batcher is stateful, create a copy of the
	// Aggregator for long-term storage.  Otherwise the
	// Meter implementation will checkpoint the aggregator
	// again, overwriting the long-lived state.
	if b.stateful {
		tmp := agg
		agg = b.AggregatorFor(desc)
		if err := agg.Merge(tmp, desc); err != nil {
			return err
		}
	}
	b.aggCheckpoint[encoded] = export.NewRecord(
		desc,
		export.NewLabels(outputLabels, encoded, b.labelEncoder),
		agg,
	)
	return nil
}

func (b *Batcher) CheckpointSet() export.CheckpointSet {
	return &checkpointSet{
		aggCheckpointMap: b.aggCheckpoint,
		labelEncoder:     b.labelEncoder,
	}
}

func (b *Batcher) FinishedCollection() {
	if !b.stateful {
		b.aggCheckpoint = aggCheckpointMap{}
	}
}

func (p *checkpointSet) ForEach(f func(export.Record)) {
	for _, entry := range p.aggCheckpointMap {
		f(entry)
	}
}

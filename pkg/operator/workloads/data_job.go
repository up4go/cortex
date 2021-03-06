/*
Copyright 2019 Cortex Labs, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workloads

import (
	"path/filepath"
	"strings"

	sparkop "github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/apis/sparkoperator.k8s.io/v1alpha1"

	"github.com/cortexlabs/cortex/pkg/api/context"
	"github.com/cortexlabs/cortex/pkg/api/userconfig"
	"github.com/cortexlabs/cortex/pkg/lib/errors"
	"github.com/cortexlabs/cortex/pkg/lib/sets/strset"
	"github.com/cortexlabs/cortex/pkg/operator/argo"
	"github.com/cortexlabs/cortex/pkg/operator/aws"
	"github.com/cortexlabs/cortex/pkg/operator/spark"
)

func dataJobSpec(
	ctx *context.Context,
	shouldIngest bool,
	rawColumns strset.Set,
	aggregates strset.Set,
	transformedColumns strset.Set,
	trainingDatasets strset.Set,
	workloadID string,
	sparkCompute *userconfig.SparkCompute,
) *sparkop.SparkApplication {

	args := []string{
		"--raw-columns=" + strings.Join(rawColumns.Slice(), ","),
		"--aggregates=" + strings.Join(aggregates.Slice(), ","),
		"--transformed-columns=" + strings.Join(transformedColumns.Slice(), ","),
		"--training-datasets=" + strings.Join(trainingDatasets.Slice(), ","),
	}
	if shouldIngest {
		args = append(args, "--ingest")
	}
	spec := spark.Spec(workloadID, ctx, workloadTypeData, sparkCompute, args...)
	argo.EnableGC(spec)
	return spec
}

func dataWorkloadSpecs(ctx *context.Context) ([]*WorkloadSpec, error) {
	workloadID := generateWorkloadID()

	rawFileExists, err := aws.IsS3File(filepath.Join(ctx.RawDataset.Key, "_SUCCESS"))
	if err != nil {
		return nil, errors.Wrap(err, ctx.App.Name, "raw dataset")
	}

	var allComputes []*userconfig.SparkCompute

	shouldIngest := !rawFileExists
	if shouldIngest {
		externalDataPath := ctx.Environment.Data.GetExternalPath()
		externalDataExists, err := aws.IsS3aPrefixExternal(externalDataPath)
		if err != nil || !externalDataExists {
			return nil, errors.Wrap(ErrorUserDataUnavailable(externalDataPath), ctx.App.Name, userconfig.Identify(ctx.Environment), userconfig.DataKey, userconfig.PathKey)
		}
		for _, rawColumn := range ctx.RawColumns {
			allComputes = append(allComputes, rawColumn.GetCompute())
		}
	}

	rawColumnIDs := strset.New()
	var rawColumns []string
	for rawColumnName, rawColumn := range ctx.RawColumns {
		isCached, err := checkResourceCached(rawColumn, ctx)
		if err != nil {
			return nil, err
		}
		if isCached {
			continue
		}
		rawColumns = append(rawColumns, rawColumnName)
		rawColumnIDs.Add(rawColumn.GetID())
		allComputes = append(allComputes, rawColumn.GetCompute())
	}

	aggregateIDs := strset.New()
	var aggregates []string
	for aggregateName, aggregate := range ctx.Aggregates {
		isCached, err := checkResourceCached(aggregate, ctx)
		if err != nil {
			return nil, err
		}
		if isCached {
			continue
		}
		aggregates = append(aggregates, aggregateName)
		aggregateIDs.Add(aggregate.GetID())
		allComputes = append(allComputes, aggregate.Compute)
	}

	transformedColumnIDs := strset.New()
	var transformedColumns []string
	for transformedColumnName, transformedColumn := range ctx.TransformedColumns {
		isCached, err := checkResourceCached(transformedColumn, ctx)
		if err != nil {
			return nil, err
		}
		if isCached {
			continue
		}
		transformedColumns = append(transformedColumns, transformedColumnName)
		transformedColumnIDs.Add(transformedColumn.GetID())
		allComputes = append(allComputes, transformedColumn.Compute)
	}

	trainingDatasetIDs := strset.New()
	var trainingDatasets []string
	for modelName, model := range ctx.Models {
		dataset := model.Dataset
		isCached, err := checkResourceCached(dataset, ctx)
		if err != nil {
			return nil, err
		}
		if isCached {
			continue
		}
		trainingDatasets = append(trainingDatasets, modelName)
		trainingDatasetIDs.Add(dataset.GetID())
		dependencyIDs := ctx.AllComputedResourceDependencies(dataset.GetID())
		for _, transformedColumn := range ctx.TransformedColumns {
			if _, ok := dependencyIDs[transformedColumn.ID]; ok {
				allComputes = append(allComputes, transformedColumn.Compute)
			}
		}
	}

	resourceIDSet := strset.Union(rawColumnIDs, aggregateIDs, transformedColumnIDs, trainingDatasetIDs)

	if !shouldIngest && len(resourceIDSet) == 0 {
		return nil, nil
	}

	sparkCompute := userconfig.MaxSparkCompute(allComputes...)
	spec := dataJobSpec(ctx, shouldIngest, rawColumnIDs, aggregateIDs, transformedColumnIDs, trainingDatasetIDs, workloadID, sparkCompute)

	workloadSpec := &WorkloadSpec{
		WorkloadID:       workloadID,
		ResourceIDs:      resourceIDSet,
		Spec:             spec,
		K8sAction:        "create",
		SuccessCondition: spark.SuccessCondition,
		FailureCondition: spark.FailureCondition,
		WorkloadType:     workloadTypeData,
	}
	return []*WorkloadSpec{workloadSpec}, nil
}

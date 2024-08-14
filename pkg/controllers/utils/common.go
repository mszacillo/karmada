/*
Copyright 2020 The Karmada Authors.
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

package utils

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/karmada-io/karmada/pkg/util/helper"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
)

func restrictFailoverHistoryInfo(binding *workv1alpha2.ResourceBinding) bool {
	placement := binding.Spec.Placement
	// Check if replica scheduling type is Duplicated
	if placement.ReplicaScheduling.ReplicaSchedulingType == policyv1alpha1.ReplicaSchedulingTypeDuplicated {
		return true
	}

	// Check if replica scheduling type is Divided with no spread constraints or invalid spread constraints
	if placement.ReplicaScheduling.ReplicaSchedulingType == policyv1alpha1.ReplicaSchedulingTypeDivided {
		if len(placement.SpreadConstraints) == 0 {
			return true
		}

		for _, spreadConstraint := range placement.SpreadConstraints {
			if spreadConstraint.SpreadByLabel != "" {
				return true
			}
			if spreadConstraint.SpreadByField == "cluster" && (spreadConstraint.MaxGroups > 1 || spreadConstraint.MinGroups > 1) {
				return true
			}
		}
	}

	return false
}

func UpdateFailoverStatus(client client.Client, binding *workv1alpha2.ResourceBinding, cluster string, failoverType string) (err error) {
	if restrictFailoverHistoryInfo(binding) {
		return nil
	}
	message := fmt.Sprintf("Failover triggered for replica on cluster %s", cluster)

	var reason string
	if failoverType == workv1alpha2.EvictionReasonApplicationFailure {
		reason = "ApplicationFailover"
	} else if failoverType == workv1alpha2.EvictionReasonTaintUntolerated {
		reason = "ClusterFailover"
	} else {
		errMsg := "Invalid failover type passed into updateFailoverStatus"
		klog.Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	newFailoverAppliedCondition := metav1.Condition{
		Type:               failoverType,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() (err error) {
		_, err = helper.UpdateStatus(context.Background(), client, binding, func() error {
			// set binding status with the newest condition
			currentTime := metav1.Now()
			failoverHistoryItem := workv1alpha2.FailoverHistoryItem{
				FailoverTime:  &currentTime,
				OriginCluster: cluster,
				Reason:        reason,
			}
			binding.Status.FailoverHistory = append(binding.Status.FailoverHistory, failoverHistoryItem)
			klog.V(4).Infof("Failover history is %+v", binding.Status.FailoverHistory)
			existingCondition := meta.FindStatusCondition(binding.Status.Conditions, failoverType)
			if existingCondition != nil && newFailoverAppliedCondition.Message == existingCondition.Message { //check
				// SetStatusCondition only updates if new status differs from the old status
				// Update the time here as the status will not change if multiple failovers of the same failoverType occur
				existingCondition.LastTransitionTime = metav1.Now()
			} else {
				meta.SetStatusCondition(&binding.Status.Conditions, newFailoverAppliedCondition)
			}
			klog.V(4).Infof("Removing cluster %s from binding. Remaining clusters are %+v", cluster, binding.Spec.Clusters)
			return nil
		})
		return err
	})

	if err != nil {
		klog.Errorf("Failed to update condition of binding %s/%s: %s", binding.Namespace, binding.Name, err.Error())
		return err
	}
	return nil
}

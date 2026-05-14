package linode

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const (
	calicoNodeLabelSelector = "k8s-app=calico-node"
	calicoNamespace         = "kube-system"
)

// calicoRoutes implements cloudprovider.Routes by checking calico-node pod
// readiness instead of managing actual cloud routes. This lets the Kubernetes
// route controller set NodeNetworkUnavailable=True until calico-node is ready
// on each node, which causes the lifecycle controller to apply the standard
// node.kubernetes.io/network-unavailable taint.
type calicoRoutes struct {
	kubeClient kubernetes.Interface
}

func newCalicoRoutes() *calicoRoutes {
	return &calicoRoutes{}
}

// ListRoutes returns a synthetic route for each node where calico-node is Ready.
// Nodes without a ready calico-node get no route, causing the route controller
// to set NodeNetworkUnavailable=True.
func (r *calicoRoutes) ListRoutes(ctx context.Context, clusterName string) ([]*cloudprovider.Route, error) {
	pods, err := r.kubeClient.CoreV1().Pods(calicoNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: calicoNodeLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list calico-node pods: %w", err)
	}

	var routes []*cloudprovider.Route
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !isCalicoNodeReady(pod) {
			klog.V(4).Infof("calico-routes: calico-node on %s not ready, skipping", pod.Spec.NodeName)
			continue
		}

		node, err := r.kubeClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			klog.Warningf("calico-routes: failed to get node %s: %v", pod.Spec.NodeName, err)
			continue
		}

		for _, cidr := range node.Spec.PodCIDRs {
			routes = append(routes, &cloudprovider.Route{
				TargetNode:      types.NodeName(node.Name),
				DestinationCIDR: cidr,
			})
		}
	}

	klog.V(4).Infof("calico-routes: returning %d routes for cluster %s", len(routes), clusterName)
	return routes, nil
}

// CreateRoute is a no-op for the calico route controller. Routes are "created"
// by calico-node becoming ready. Returns an error if calico-node is not ready
// on the target node so the route controller retries on the next reconcile cycle.
func (r *calicoRoutes) CreateRoute(ctx context.Context, clusterName string, nameHint string, route *cloudprovider.Route) error {
	pods, err := r.kubeClient.CoreV1().Pods(calicoNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: calicoNodeLabelSelector,
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", route.TargetNode),
	})
	if err != nil {
		return fmt.Errorf("calico-routes: failed to list calico-node pods on %s: %w", route.TargetNode, err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("calico-routes: calico-node not found on node %s", route.TargetNode)
	}

	if !isCalicoNodeReady(&pods.Items[0]) {
		return fmt.Errorf("calico-routes: calico-node not ready on node %s", route.TargetNode)
	}

	klog.V(3).Infof("calico-routes: calico-node ready on %s, route created for %s", route.TargetNode, route.DestinationCIDR)
	return nil
}

// DeleteRoute is a no-op. There are no actual cloud routes to delete.
func (r *calicoRoutes) DeleteRoute(ctx context.Context, clusterName string, route *cloudprovider.Route) error {
	klog.V(4).Infof("calico-routes: DeleteRoute no-op for %s on %s", route.DestinationCIDR, route.TargetNode)
	return nil
}

func isCalicoNodeReady(pod *v1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == v1.PodReady && c.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

package linode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	cloudprovider "k8s.io/cloud-provider"
)

func newReadyCalicoNodePod(nodeName string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-node-" + nodeName,
			Namespace: calicoNamespace,
			Labels:    map[string]string{"k8s-app": "calico-node"},
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{Type: v1.PodReady, Status: v1.ConditionTrue},
			},
		},
	}
}

func newNotReadyCalicoNodePod(nodeName string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-node-" + nodeName,
			Namespace: calicoNamespace,
			Labels:    map[string]string{"k8s-app": "calico-node"},
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{Type: v1.PodReady, Status: v1.ConditionFalse},
			},
		},
	}
}

func newNodeWithPodCIDRs(name string, cidrs []string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.NodeSpec{
			PodCIDRs: cidrs,
			PodCIDR:  cidrs[0],
		},
	}
}

func TestCalicoRoutes_ListRoutes(t *testing.T) {
	t.Run("returns empty when no calico-node pods exist", func(t *testing.T) {
		ctx := t.Context()
		client := fake.NewSimpleClientset()
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		assert.Empty(t, routes)
	})

	t.Run("returns empty when calico-node is not ready", func(t *testing.T) {
		ctx := t.Context()
		node := newNodeWithPodCIDRs("node-1", []string{"10.244.0.0/24"})
		pod := newNotReadyCalicoNodePod("node-1")

		client := fake.NewSimpleClientset(node, pod)
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		assert.Empty(t, routes)
	})

	t.Run("returns route when calico-node is ready", func(t *testing.T) {
		ctx := t.Context()
		node := newNodeWithPodCIDRs("node-1", []string{"10.244.0.0/24"})
		pod := newReadyCalicoNodePod("node-1")

		client := fake.NewSimpleClientset(node, pod)
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		require.Len(t, routes, 1)
		assert.Equal(t, types.NodeName("node-1"), routes[0].TargetNode)
		assert.Equal(t, "10.244.0.0/24", routes[0].DestinationCIDR)
	})

	t.Run("returns routes for multiple PodCIDRs (dual-stack)", func(t *testing.T) {
		ctx := t.Context()
		node := newNodeWithPodCIDRs("node-1", []string{"10.244.0.0/24", "fd00:10:244::/64"})
		pod := newReadyCalicoNodePod("node-1")

		client := fake.NewSimpleClientset(node, pod)
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		require.Len(t, routes, 2)
		assert.Equal(t, "10.244.0.0/24", routes[0].DestinationCIDR)
		assert.Equal(t, "fd00:10:244::/64", routes[1].DestinationCIDR)
	})

	t.Run("returns routes only for nodes with ready calico-node", func(t *testing.T) {
		ctx := t.Context()
		node1 := newNodeWithPodCIDRs("node-1", []string{"10.244.0.0/24"})
		node2 := newNodeWithPodCIDRs("node-2", []string{"10.244.1.0/24"})
		node3 := newNodeWithPodCIDRs("node-3", []string{"10.244.2.0/24"})
		pod1 := newReadyCalicoNodePod("node-1")
		pod2 := newNotReadyCalicoNodePod("node-2")
		pod3 := newReadyCalicoNodePod("node-3")

		client := fake.NewSimpleClientset(node1, node2, node3, pod1, pod2, pod3)
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		require.Len(t, routes, 2)

		nodeNames := make(map[types.NodeName]bool)
		for _, r := range routes {
			nodeNames[r.TargetNode] = true
		}
		assert.True(t, nodeNames["node-1"])
		assert.False(t, nodeNames["node-2"])
		assert.True(t, nodeNames["node-3"])
	})

	t.Run("skips node when node object is missing", func(t *testing.T) {
		ctx := t.Context()
		// Pod exists but its node doesn't
		pod := newReadyCalicoNodePod("ghost-node")

		client := fake.NewSimpleClientset(pod)
		rc := &calicoRoutes{kubeClient: client}

		routes, err := rc.ListRoutes(ctx, "test-cluster")
		require.NoError(t, err)
		assert.Empty(t, routes)
	})
}

func TestCalicoRoutes_CreateRoute(t *testing.T) {
	t.Run("succeeds when calico-node is ready", func(t *testing.T) {
		ctx := t.Context()
		pod := newReadyCalicoNodePod("node-1")

		client := fake.NewSimpleClientset(pod)
		rc := &calicoRoutes{kubeClient: client}

		err := rc.CreateRoute(ctx, "test-cluster", "", &cloudprovider.Route{
			TargetNode:      "node-1",
			DestinationCIDR: "10.244.0.0/24",
		})
		assert.NoError(t, err)
	})

	t.Run("fails when calico-node is not ready", func(t *testing.T) {
		ctx := t.Context()
		pod := newNotReadyCalicoNodePod("node-1")

		client := fake.NewSimpleClientset(pod)
		rc := &calicoRoutes{kubeClient: client}

		err := rc.CreateRoute(ctx, "test-cluster", "", &cloudprovider.Route{
			TargetNode:      "node-1",
			DestinationCIDR: "10.244.0.0/24",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})

	t.Run("fails when calico-node pod does not exist", func(t *testing.T) {
		ctx := t.Context()
		client := fake.NewSimpleClientset()
		rc := &calicoRoutes{kubeClient: client}

		err := rc.CreateRoute(ctx, "test-cluster", "", &cloudprovider.Route{
			TargetNode:      "node-1",
			DestinationCIDR: "10.244.0.0/24",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestCalicoRoutes_DeleteRoute(t *testing.T) {
	t.Run("always succeeds (no-op)", func(t *testing.T) {
		ctx := t.Context()
		client := fake.NewSimpleClientset()
		rc := &calicoRoutes{kubeClient: client}

		err := rc.DeleteRoute(ctx, "test-cluster", &cloudprovider.Route{
			TargetNode:      "node-1",
			DestinationCIDR: "10.244.0.0/24",
		})
		assert.NoError(t, err)
	})
}

func TestIsCalicoNodeReady(t *testing.T) {
	t.Run("returns true when PodReady is True", func(t *testing.T) {
		pod := &v1.Pod{
			Status: v1.PodStatus{
				Conditions: []v1.PodCondition{
					{Type: v1.PodReady, Status: v1.ConditionTrue},
				},
			},
		}
		assert.True(t, isCalicoNodeReady(pod))
	})

	t.Run("returns false when PodReady is False", func(t *testing.T) {
		pod := &v1.Pod{
			Status: v1.PodStatus{
				Conditions: []v1.PodCondition{
					{Type: v1.PodReady, Status: v1.ConditionFalse},
				},
			},
		}
		assert.False(t, isCalicoNodeReady(pod))
	})

	t.Run("returns false when no conditions exist", func(t *testing.T) {
		pod := &v1.Pod{}
		assert.False(t, isCalicoNodeReady(pod))
	})

	t.Run("returns false when PodReady condition is missing", func(t *testing.T) {
		pod := &v1.Pod{
			Status: v1.PodStatus{
				Conditions: []v1.PodCondition{
					{Type: v1.PodInitialized, Status: v1.ConditionTrue},
				},
			},
		}
		assert.False(t, isCalicoNodeReady(pod))
	})
}

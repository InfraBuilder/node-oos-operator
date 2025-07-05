package main

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type NodeOperator struct {
	clientset  kubernetes.Interface
	logger     *logrus.Logger
	nodeStates map[string]*NodeState
	workqueue  workqueue.RateLimitingInterface
	controller cache.Controller
	indexer    cache.Indexer
}

type NodeState struct {
	Name              string
	IsReady           bool
	LastReadyTime     time.Time
	LastNotReadyTime  time.Time
	IsTainted         bool
	NotReadyDuration  time.Duration
	LastMetricsUpdate time.Time
}

func NewNodeOperator(clientset kubernetes.Interface, logger *logrus.Logger) *NodeOperator {
	return &NodeOperator{
		clientset:  clientset,
		logger:     logger,
		nodeStates: make(map[string]*NodeState),
		workqueue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodes"),
	}
}

func (o *NodeOperator) Run(ctx context.Context) error {
	defer o.workqueue.ShutDown()

	// Create a node watcher
	watchlist := cache.NewListWatchFromClient(
		o.clientset.CoreV1().RESTClient(),
		"nodes",
		metav1.NamespaceAll,
		fields.Everything(),
	)

	// Create the controller
	o.indexer, o.controller = cache.NewIndexerInformer(
		watchlist,
		&corev1.Node{},
		time.Second*30,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if node, ok := obj.(*corev1.Node); ok {
					o.workqueue.Add(node.Name)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				if node, ok := newObj.(*corev1.Node); ok {
					o.workqueue.Add(node.Name)
				}
			},
			DeleteFunc: func(obj interface{}) {
				if node, ok := obj.(*corev1.Node); ok {
					o.handleNodeDeletion(node.Name)
				}
			},
		},
		cache.Indexers{},
	)

	// Start the controller
	go o.controller.Run(ctx.Done())

	// Wait for the cache to sync
	if !cache.WaitForCacheSync(ctx.Done(), o.controller.HasSynced) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	o.logger.Info("Cache synced, starting workers")

	// Start workers
	go o.runWorker(ctx)
	go o.runMetricsUpdater(ctx)

	<-ctx.Done()
	o.logger.Info("Shutting down operator")
	return nil
}

func (o *NodeOperator) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if obj, shutdown := o.workqueue.Get(); shutdown {
				return
			} else {
				func() {
					defer o.workqueue.Done(obj)
					if nodeName, ok := obj.(string); ok {
						if err := o.processNode(nodeName); err != nil {
							o.logger.WithError(err).WithField("node", nodeName).Error("Failed to process node")
							o.workqueue.AddRateLimited(obj)
						} else {
							o.workqueue.Forget(obj)
						}
					}
				}()
			}
		}
	}
}

func (o *NodeOperator) processNode(nodeName string) error {
	obj, exists, err := o.indexer.GetByKey(nodeName)
	if err != nil {
		return fmt.Errorf("failed to get node %s from indexer: %w", nodeName, err)
	}
	if !exists {
		o.logger.WithField("node", nodeName).Info("Node no longer exists")
		return nil
	}

	node, ok := obj.(*corev1.Node)
	if !ok {
		return fmt.Errorf("object is not a Node: %T", obj)
	}

	return o.handleNodeUpdate(node)
}

func (o *NodeOperator) handleNodeUpdate(node *corev1.Node) error {
	nodeName := node.Name
	isReady := o.isNodeReady(node)
	currentTime := time.Now()

	// Initialize node state if it doesn't exist
	if _, exists := o.nodeStates[nodeName]; !exists {
		o.nodeStates[nodeName] = &NodeState{
			Name:              nodeName,
			IsReady:           isReady,
			LastReadyTime:     currentTime,
			LastNotReadyTime:  currentTime,
			IsTainted:         o.hasOutOfServiceTaint(node),
			LastMetricsUpdate: currentTime,
		}
		if isReady {
			o.nodeStates[nodeName].LastReadyTime = currentTime
		} else {
			o.nodeStates[nodeName].LastNotReadyTime = currentTime
		}
	}

	nodeState := o.nodeStates[nodeName]
	previousReady := nodeState.IsReady
	nodeState.IsReady = isReady
	nodeState.IsTainted = o.hasOutOfServiceTaint(node)

	// Update timestamps based on state changes
	if isReady && !previousReady {
		// Node became ready
		nodeState.LastReadyTime = currentTime
		o.logger.WithField("node", nodeName).Info("Node became ready")
	} else if !isReady && previousReady {
		// Node became not ready
		nodeState.LastNotReadyTime = currentTime
		o.logger.WithField("node", nodeName).Info("Node became not ready")
	}

	// Update not ready duration
	if !isReady {
		nodeState.NotReadyDuration = currentTime.Sub(nodeState.LastNotReadyTime)
	} else {
		nodeState.NotReadyDuration = 0
	}

	// Apply taint logic
	if !isReady && !nodeState.IsTainted && nodeState.NotReadyDuration >= notReadyThreshold {
		// Node has been not ready for too long, apply taint
		if err := o.applyOutOfServiceTaint(node); err != nil {
			return fmt.Errorf("failed to apply taint to node %s: %w", nodeName, err)
		}
		nodeState.IsTainted = true
		o.logger.WithFields(logrus.Fields{
			"node":     nodeName,
			"duration": nodeState.NotReadyDuration,
		}).Info("Applied out-of-service taint to node")
	} else if isReady && nodeState.IsTainted {
		// Node is ready and tainted, check if it's been ready long enough
		readyDuration := currentTime.Sub(nodeState.LastReadyTime)
		if readyDuration >= recoveryThreshold {
			// Node has been ready for enough time, remove taint
			if err := o.removeOutOfServiceTaint(node); err != nil {
				return fmt.Errorf("failed to remove taint from node %s: %w", nodeName, err)
			}
			nodeState.IsTainted = false
			o.logger.WithFields(logrus.Fields{
				"node":     nodeName,
				"duration": readyDuration,
			}).Info("Removed out-of-service taint from node")
		}
	}

	return nil
}

func (o *NodeOperator) handleNodeDeletion(nodeName string) {
	delete(o.nodeStates, nodeName)
	o.logger.WithField("node", nodeName).Info("Node deleted, removed from tracking")
}

func (o *NodeOperator) isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (o *NodeOperator) hasOutOfServiceTaint(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == OutOfServiceTaint && taint.Value == TaintValue && taint.Effect == TaintEffect {
			return true
		}
	}
	return false
}

func (o *NodeOperator) applyOutOfServiceTaint(node *corev1.Node) error {
	// Create a copy of the node to modify
	nodeCopy := node.DeepCopy()

	// Check if taint already exists
	for _, taint := range nodeCopy.Spec.Taints {
		if taint.Key == OutOfServiceTaint && taint.Value == TaintValue && taint.Effect == TaintEffect {
			return nil // Taint already exists
		}
	}

	// Add the taint
	newTaint := corev1.Taint{
		Key:    OutOfServiceTaint,
		Value:  TaintValue,
		Effect: TaintEffect,
	}
	nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, newTaint)

	// Update the node
	_, err := o.clientset.CoreV1().Nodes().Update(context.TODO(), nodeCopy, metav1.UpdateOptions{})
	return err
}

func (o *NodeOperator) removeOutOfServiceTaint(node *corev1.Node) error {
	// Create a copy of the node to modify
	nodeCopy := node.DeepCopy()

	// Remove the taint
	var newTaints []corev1.Taint
	for _, taint := range nodeCopy.Spec.Taints {
		if !(taint.Key == OutOfServiceTaint && taint.Value == TaintValue && taint.Effect == TaintEffect) {
			newTaints = append(newTaints, taint)
		}
	}
	nodeCopy.Spec.Taints = newTaints

	// Update the node
	_, err := o.clientset.CoreV1().Nodes().Update(context.TODO(), nodeCopy, metav1.UpdateOptions{})
	return err
}

func (o *NodeOperator) runMetricsUpdater(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.updateMetrics()
		}
	}
}

func (o *NodeOperator) updateMetrics() {
	currentTime := time.Now()

	for nodeName, nodeState := range o.nodeStates {
		// Update counter for not ready seconds
		if !nodeState.IsReady {
			timeSinceLastUpdate := currentTime.Sub(nodeState.LastMetricsUpdate)
			nodeNotReadySeconds.WithLabelValues(nodeName).Add(timeSinceLastUpdate.Seconds())
		}

		// Update current readiness gauge
		if nodeState.IsReady {
			nodeCurrentlyNotReady.WithLabelValues(nodeName).Set(0)
		} else {
			nodeCurrentlyNotReady.WithLabelValues(nodeName).Set(1)
		}

		// Update taint status gauge
		if nodeState.IsTainted {
			nodesTainted.WithLabelValues(nodeName).Set(1)
		} else {
			nodesTainted.WithLabelValues(nodeName).Set(0)
		}

		nodeState.LastMetricsUpdate = currentTime
	}
}

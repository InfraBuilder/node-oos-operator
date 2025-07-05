package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	kubeconfig            string
	notReadyThreshold     time.Duration
	recoveryThreshold     time.Duration
	metricsAddr           string
	nodeNotReadySeconds   *prometheus.CounterVec
	nodeCurrentlyNotReady *prometheus.GaugeVec
	nodesTainted          *prometheus.GaugeVec
	logger                *logrus.Logger
)

const (
	OutOfServiceTaint = "node.kubernetes.io/out-of-service"
	TaintEffect       = "NoExecute"
	TaintValue        = "shutdown"
)

func init() {
	logger = logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetOutput(os.Stdout)
	logger.SetLevel(logrus.InfoLevel)

	nodeNotReadySeconds = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nodehealth_node_not_ready_seconds_total",
			Help: "Total number of seconds a node has been not ready",
		},
		[]string{"node"},
	)

	nodeCurrentlyNotReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nodehealth_node_currently_not_ready",
			Help: "Current status of node readiness (1 = not ready, 0 = ready)",
		},
		[]string{"node"},
	)

	nodesTainted = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nodehealth_node_tainted",
			Help: "Current taint status of node (1 = tainted, 0 = not tainted)",
		},
		[]string{"node"},
	)

	prometheus.MustRegister(nodeNotReadySeconds, nodeCurrentlyNotReady, nodesTainted)
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "node-oos-operator",
		Short: "Kubernetes operator that manages node out-of-service taints",
		Long:  "Monitors node health and applies/removes out-of-service taints based on readiness status",
		Run:   runOperator,
	}

	rootCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	rootCmd.Flags().StringVar(&metricsAddr, "metrics-addr", ":8080", "Address to serve metrics on")

	if err := rootCmd.Execute(); err != nil {
		logger.WithError(err).Fatal("Failed to start operator")
	}
}

func runOperator(cmd *cobra.Command, args []string) {
	// Load configuration from environment variables
	notReadyThresholdStr := os.Getenv("NOT_READY_THRESHOLD")
	if notReadyThresholdStr == "" {
		notReadyThresholdStr = "300s" // Default 5 minutes
	}
	var err error
	notReadyThreshold, err = time.ParseDuration(notReadyThresholdStr)
	if err != nil {
		logger.WithError(err).Fatal("Invalid NOT_READY_THRESHOLD format")
	}

	recoveryThresholdStr := os.Getenv("RECOVERY_THRESHOLD")
	if recoveryThresholdStr == "" {
		recoveryThresholdStr = "60s" // Default 1 minute
	}
	recoveryThreshold, err = time.ParseDuration(recoveryThresholdStr)
	if err != nil {
		logger.WithError(err).Fatal("Invalid RECOVERY_THRESHOLD format")
	}

	logger.WithFields(logrus.Fields{
		"not_ready_threshold_seconds": notReadyThreshold.Seconds(),
		"recovery_threshold_seconds":  recoveryThreshold.Seconds(),
		"metrics_addr":                metricsAddr,
	}).Info("Starting node out-of-service automator")

	// Create Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		logger.WithError(err).Fatal("Failed to get kubeconfig")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kubernetes client")
	}

	// Start metrics server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		logger.WithField("addr", metricsAddr).Info("Starting metrics server")
		if err := http.ListenAndServe(metricsAddr, nil); err != nil {
			logger.WithError(err).Fatal("Failed to start metrics server")
		}
	}()

	// Create operator and start monitoring
	operator := NewNodeOperator(clientset, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Received shutdown signal, shutting down gracefully...")
		cancel()
	}()

	// Start the operator
	if err := operator.Run(ctx); err != nil {
		logger.WithError(err).Fatal("Operator failed")
	}
}

func getKubeConfig() (*rest.Config, error) {
	// If kubeconfig flag is provided, use it
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fallback to default kubeconfig location
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(kubeconfig); err == nil {
			return clientcmd.BuildConfigFromFlags("", kubeconfig)
		}
	}

	return nil, fmt.Errorf("unable to find valid kubeconfig")
}

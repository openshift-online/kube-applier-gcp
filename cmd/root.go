package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"

	"k8s.io/klog/v2"

	"github.com/openshift-online/kube-applier-gcp/internal/database"
	"github.com/openshift-online/kube-applier-gcp/internal/database/informers"
	"github.com/openshift-online/kube-applier-gcp/pkg/app"
)

// KubeApplierRootCmdFlags collects the user-facing flags for the kube-applier
// binary. Required values must be supplied as flags; the binary does not read
// from environment variables so a misconfigured pod fails fast.
type KubeApplierRootCmdFlags struct {
	Kubeconfig                 string
	KubeNamespace              string
	ManagementCluster          string
	FirestoreProject           string
	FirestoreSpecsDatabase     string
	FirestoreStatusDatabase    string
	MetricsServerListenAddress string
	HealthzServerListenAddress string
	LeaderElectionID           string
	LogVerbosity               int
	ExitOnPanic                bool
}

func (f *KubeApplierRootCmdFlags) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", f.Kubeconfig,
		"Absolute path to the kubeconfig file. Empty selects the in-cluster config.")
	cmd.Flags().StringVar(&f.KubeNamespace, "namespace", f.KubeNamespace,
		"Kubernetes namespace that hosts the leader-election lease.")
	cmd.Flags().StringVar(&f.ManagementCluster, "management-cluster", f.ManagementCluster,
		"Name of the GKE management cluster this pod runs in.")
	cmd.Flags().StringVar(&f.FirestoreProject, "firestore-project", f.FirestoreProject,
		"GCP project ID that hosts the Firestore databases.")
	cmd.Flags().StringVar(&f.FirestoreSpecsDatabase, "firestore-specs-database", f.FirestoreSpecsDatabase,
		"Firestore named database ID for spec documents (read-only by agent).")
	cmd.Flags().StringVar(&f.FirestoreStatusDatabase, "firestore-status-database", f.FirestoreStatusDatabase,
		"Firestore named database ID for status documents (read-write by agent).")
	cmd.Flags().StringVar(&f.MetricsServerListenAddress, "metrics-listen-address", f.MetricsServerListenAddress,
		"Address on which to expose Prometheus metrics.")
	cmd.Flags().StringVar(&f.HealthzServerListenAddress, "healthz-listen-address", f.HealthzServerListenAddress,
		"Address on which to expose the /healthz endpoint.")
	cmd.Flags().StringVar(&f.LeaderElectionID, "leader-election-id", f.LeaderElectionID,
		"Lease name used for leader election within --namespace.")
	cmd.Flags().IntVar(&f.LogVerbosity, "log-verbosity", f.LogVerbosity,
		"Log verbosity. 0 is INFO; higher values are more verbose.")
	cmd.Flags().BoolVar(&f.ExitOnPanic, "exit-on-panic", f.ExitOnPanic,
		"If set, the process exits on any goroutine panic via apimachinery's HandleCrash.")

	for _, name := range []string{"namespace", "management-cluster", "firestore-project"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Errorf("MarkFlagRequired(%q): %w", name, err))
		}
	}
}

func (f *KubeApplierRootCmdFlags) validate() error {
	if len(f.ManagementCluster) == 0 {
		return fmt.Errorf("--management-cluster must not be empty")
	}
	if len(f.FirestoreProject) == 0 {
		return fmt.Errorf("--firestore-project must not be empty")
	}
	if len(f.KubeNamespace) == 0 {
		return fmt.Errorf("--namespace must not be empty")
	}
	if len(f.LeaderElectionID) == 0 {
		return fmt.Errorf("--leader-election-id must not be empty")
	}
	if f.LogVerbosity < 0 {
		return fmt.Errorf("--log-verbosity must be >= 0")
	}
	return nil
}

// ToKubeApplierOptions resolves flags into wired Options that the app layer
// consumes. Each external dependency is constructed here so that Run() never
// sees raw flag values.
func (f *KubeApplierRootCmdFlags) ToKubeApplierOptions(ctx context.Context) (*app.Options, error) {
	kubeconfig, err := app.NewKubeconfig(f.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes configuration: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %w", err)
	}
	leaderElectionLock, err := app.NewLeaderElectionLock(hostname, kubeconfig, f.KubeNamespace, f.LeaderElectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create leader election lock: %w", err)
	}

	specsDatabaseID := f.FirestoreSpecsDatabase
	if specsDatabaseID == "" {
		specsDatabaseID = "specs"
	}
	statusDatabaseID := f.FirestoreStatusDatabase
	if statusDatabaseID == "" {
		statusDatabaseID = "status"
	}

	specsClient, err := app.NewFirestoreClient(ctx, f.FirestoreProject, specsDatabaseID)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firestore specs client: %w", err)
	}
	statusClient, err := app.NewFirestoreClient(ctx, f.FirestoreProject, statusDatabaseID)
	if err != nil {
		specsClient.Close()
		return nil, fmt.Errorf("failed to create Firestore status client: %w", err)
	}

	dbClient := database.NewFirestoreKubeApplierDBClient(specsClient, statusClient)
	firestoreInformers := informers.NewKubeApplierInformers(specsClient)

	dyn, err := app.NewDynamicClient(kubeconfig)
	if err != nil {
		specsClient.Close()
		statusClient.Close()
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &app.Options{
		ManagementCluster:          f.ManagementCluster,
		LeaderElectionLock:         leaderElectionLock,
		KubeApplierDBClient:        dbClient,
		Informers:                  firestoreInformers,
		DynamicClient:              dyn,
		MetricsServerListenAddress: f.MetricsServerListenAddress,
		HealthzServerListenAddress: f.HealthzServerListenAddress,
		ExitOnPanic:                f.ExitOnPanic,
	}, nil
}

func NewKubeApplierRootCmdFlags() *KubeApplierRootCmdFlags {
	return &KubeApplierRootCmdFlags{
		MetricsServerListenAddress: ":8081",
		HealthzServerListenAddress: ":8083",
		LeaderElectionID:           "kube-applier",
		LogVerbosity:               0,
		ExitOnPanic:                true,
	}
}

func NewCmdRoot() *cobra.Command {
	processName := filepath.Base(os.Args[0])

	flags := NewKubeApplierRootCmdFlags()

	cmd := &cobra.Command{
		Use:   processName,
		Args:  cobra.NoArgs,
		Short: app.AppShortDescriptionName,
		Long: fmt.Sprintf(`%s

  The kube-applier reconciles ApplyDesire, DeleteDesire, and ReadDesire
  documents stored in Firestore against the management cluster's local
  kube-apiserver.

  # Run kube-applier locally pointing at a dev Firestore database and the
  # in-cluster kubeconfig.
  %s --management-cluster ${MC_NAME} \
      --firestore-project ${PROJECT_ID} \
      --namespace ${NAMESPACE}
`, app.AppShortDescriptionName, processName),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunRootCmd(cmd, flags)
		},
		SilenceErrors: true,
	}

	cmd.SetErrPrefix(cmd.Short + " error:")
	flags.AddFlags(cmd)

	return cmd
}

func RunRootCmd(cmd *cobra.Command, flags *KubeApplierRootCmdFlags) error {
	if err := flags.validate(); err != nil {
		return fmt.Errorf("flags validation failed: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handlerOptions := &slog.HandlerOptions{Level: slog.Level(flags.LogVerbosity * -1), AddSource: true}
	slogJSONHandler := slog.NewJSONHandler(os.Stdout, handlerOptions)
	logger := logr.FromSlogHandler(slogJSONHandler)
	ctx = klog.NewContext(ctx, logger)
	klog.SetLogger(logger)

	options, err := flags.ToKubeApplierOptions(ctx)
	if err != nil {
		return fmt.Errorf("failed to convert flags to options: %w", err)
	}

	if err := options.Run(ctx); err != nil {
		return fmt.Errorf("failed to run kube-applier: %w", err)
	}
	return nil
}

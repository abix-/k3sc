package cmd

import (
	"fmt"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/operator"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func init() {
	rootCmd.AddCommand(operatorCmd)
}

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Run the k3sc operator (long-running controller)",
	RunE:  runOperator,
}

func runOperator(cmd *cobra.Command, args []string) error {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	operator.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	cs, err := k8s.NewClient()
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}

	template, err := dispatch.LoadTemplate()
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}

	reconciler := &operator.Reconciler{
		Client:   mgr.GetClient(),
		K8s:      cs,
		Template: template,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup controller: %w", err)
	}

	ctx := ctrl.SetupSignalHandler()
	go operator.Scanner(ctx, mgr.GetClient(), types.Namespace)

	fmt.Println("[operator] starting")
	return mgr.Start(ctx)
}

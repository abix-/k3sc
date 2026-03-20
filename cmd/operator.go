package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/operator"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var operatorVerbose bool

func init() {
	operatorCmd.Flags().BoolVar(&operatorVerbose, "verbose", false, "enable verbose structured logging")
	rootCmd.AddCommand(operatorCmd)
}

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Run the k3sc operator (long-running controller)",
	RunE:  runOperator,
}

func runOperator(cmd *cobra.Command, args []string) error {
	operator.Verbose = operatorVerbose

	edt := time.FixedZone("EDT", -4*3600)
	timeEncoder := func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.In(edt).Format("15:04:05"))
	}

	// verbose: debug level, full structured output
	// normal: warn level only (suppresses INFO/DEBUG spam from controller-runtime)
	logLevel := zapcore.WarnLevel
	if operatorVerbose {
		logLevel = zapcore.DebugLevel
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.RawZapOpts(
		uberzap.WrapCore(func(c zapcore.Core) zapcore.Core {
			cfg := zapcore.EncoderConfig{
				TimeKey:        "T",
				LevelKey:       "L",
				NameKey:        "N",
				CallerKey:      "",
				MessageKey:     "M",
				StacktraceKey:  "S",
				LineEnding:     zapcore.DefaultLineEnding,
				EncodeLevel:    zapcore.CapitalLevelEncoder,
				EncodeTime:     timeEncoder,
				EncodeDuration: zapcore.StringDurationEncoder,
				EncodeName:     zapcore.FullNameEncoder,
			}
			return zapcore.NewCore(
				zapcore.NewConsoleEncoder(cfg),
				zapcore.Lock(zapcore.AddSync(os.Stderr)),
				logLevel,
			)
		}),
	)))

	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	operator.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				types.Namespace: {},
			},
		},
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

	dispatcher := &operator.DispatchReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		K8s:       cs,
		Namespace: types.Namespace,
	}
	if err := dispatcher.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup scheduler: %w", err)
	}

	ctx := ctrl.SetupSignalHandler()
	bootstrap, err := crclient.New(ctrl.GetConfigOrDie(), crclient.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("bootstrap client: %w", err)
	}
	if err := operator.EnsureDispatchState(ctx, bootstrap, types.Namespace); err != nil {
		return fmt.Errorf("ensure dispatch state: %w", err)
	}

	fmt.Println("[operator] starting")
	return mgr.Start(ctx)
}
